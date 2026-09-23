package service

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianhutter/agentctx/internal/bundle"
	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/identity"
	"github.com/sebastianhutter/agentctx/internal/store"
)

var now = time.Date(2026, 9, 19, 14, 32, 7, 0, time.Local)

// newService builds a Service over a temp vault with a fake environment, so no
// test touches the real vault, the real bindings or the real environment.
func newService(t *testing.T, env map[string]string) *Service {
	t.Helper()
	vaultDir, stateDir := t.TempDir(), t.TempDir()

	cfg := &config.Config{
		VaultPath: vaultDir, StateDir: stateDir,
		Stale:      config.StaleConfig{MaxAgeDays: 30},
		Confidence: config.ConfidenceConfig{DeriveFromSource: true},
	}

	fakeEnv := identity.Env{Getenv: func(k string) string { return env[k] }}
	st := store.New(vaultDir)
	s := &Service{
		Config: cfg, Store: st, Assembly: bundle.New(st, 30),
		Env: fakeEnv, Transport: TransportStdio,
	}
	s.SetClock(func() time.Time { return now })
	return s
}

func openProject(t *testing.T, s *Service) {
	t.Helper()
	_, err := s.OpenProject(OpenProjectRequest{
		ID: "proj", Name: "Example project",
		JiraProjects: []string{"PROJ"}, Repos: []string{"acme/infrastructure"},
	})
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
}

func TestOpenProjectCreatesThenReturns(t *testing.T) {
	s := newService(t, nil)

	b, err := s.OpenProject(OpenProjectRequest{
		ID: "proj", Name: "Example project", JiraProjects: []string{"PROJ"},
	})
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	if !b.Created {
		t.Error("first OpenProject did not report creation")
	}

	again, err := s.OpenProject(OpenProjectRequest{ID: "proj"})
	if err != nil {
		t.Fatalf("second OpenProject: %v", err)
	}
	if again.Created {
		t.Error("second OpenProject reported creation")
	}
}

// Projects are deliberate. Naming one that does not exist, with nothing to
// describe it, must refuse rather than invent an empty corpus.
func TestOpenProjectRefusesBlindCreation(t *testing.T) {
	s := newService(t, nil)
	_, err := s.OpenProject(OpenProjectRequest{ID: "proj"})
	if err == nil || !strings.Contains(err.Error(), "does not exist yet") {
		t.Fatalf("OpenProject returned %v, want a refusal", err)
	}
}

func TestOpenTaskCreatesFromSeed(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	b, err := s.OpenTask(OpenTaskRequest{
		TaskID: "proj-142",
		Seed: &Seed{
			Source: "jira", SourceKey: "PROJ-142",
			SourceURL: "https://example.atlassian.net/browse/PROJ-142",
			Title:     "Example task", Repos: []string{"acme/infrastructure"},
		},
	})
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	if !b.Created {
		t.Error("OpenTask did not report creating the note")
	}
	// The first fact about a task is where it came from.
	opened, err := s.Store.Entries("proj", store.EntryFilter{Task: "proj-142", Kinds: []string{store.KindOpened}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(opened) != 1 || !strings.Contains(opened[0].Body, "PROJ-142") {
		t.Errorf("opened entry = %+v", opened)
	}
}

// The ingest rule: no credentials in agentctx, so the agent must supply the
// facts. Enforced by refusing, not by asking.
func TestOpenTaskRefusesCreationWithoutSeed(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	_, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"})
	if err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("OpenTask returned %v, want a seed refusal", err)
	}
	if !strings.Contains(err.Error(), "no tracker credentials") {
		t.Errorf("refusal does not explain why: %v", err)
	}
}

func TestOpenTaskRefusesUnknownProject(t *testing.T) {
	s := newService(t, nil)

	_, err := s.OpenTask(OpenTaskRequest{TaskID: "nope-1", Seed: &Seed{Title: "x"}})
	if !errors.Is(err, store.ErrNoProject) {
		t.Fatalf("OpenTask returned %v, want ErrNoProject", err)
	}
	if !strings.Contains(err.Error(), "open_project") {
		t.Errorf("refusal does not say what to do: %v", err)
	}
}

// An existing note is never rewritten — that is what keeps parallel sessions on
// a synced vault safe. Re-opening appends instead.
func TestOpenTaskDoesNotRewriteTheNote(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	if _, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142", Seed: &Seed{Title: "Example task"}}); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	path := s.Store.TaskPath("proj", "proj-142")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	beforeStat, _ := os.Stat(path)

	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"})
	if err != nil {
		t.Fatalf("second OpenTask: %v", err)
	}
	if b.Created {
		t.Error("second OpenTask reported creation")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the task note was rewritten on open")
	}
	afterStat, _ := os.Stat(path)
	if !beforeStat.ModTime().Equal(afterStat.ModTime()) {
		t.Error("the task note was touched on open")
	}

	opened, err := s.Store.Entries("proj", store.EntryFilter{Task: "proj-142", Kinds: []string{store.KindOpened}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(opened) != 2 {
		t.Errorf("got %d opened entries, want one per open", len(opened))
	}
}

func TestLogDecisionRequiresRationale(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "chose B"}); err == nil {
		t.Fatal("LogDecision accepted a decision with no rationale")
	}
}

func TestLogDecisionComposesBody(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	entry, err := s.LogDecision(LogDecisionRequest{
		TaskID: "proj-142", Decision: "chose option B", Rationale: "cheaper to run",
		AlternativesRejected: []string{"option A — too slow"}, AffectedRepos: []string{"acme/infrastructure"},
	})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	for _, want := range []string{"chose option B", "Rationale", "cheaper to run", "option A", "acme/infrastructure"} {
		if !strings.Contains(entry.Body, want) {
			t.Errorf("body is missing %q:\n%s", want, entry.Body)
		}
	}
}

func TestLogFindingValidatesProvenance(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	t.Run("rejects an unknown source kind", func(t *testing.T) {
		_, err := s.LogFinding(LogFindingRequest{
			TargetID: "proj-142", Finding: "x", Source: store.Source{Kind: "vibes"},
		})
		if err == nil || !strings.Contains(err.Error(), "source.kind") {
			t.Fatalf("LogFinding returned %v, want a source.kind error", err)
		}
	})

	t.Run("repo scope requires a repo", func(t *testing.T) {
		_, err := s.LogFinding(LogFindingRequest{
			TargetID: "proj-142", Finding: "x", Scope: store.ScopeRepo,
			Source: store.Source{Kind: store.SourceFile, Ref: "a@1"},
		})
		if err == nil || !strings.Contains(err.Error(), "repo is required") {
			t.Fatalf("LogFinding returned %v, want a missing-repo error", err)
		}
	})

	t.Run("defaults the retrieval date to today", func(t *testing.T) {
		entry, err := s.LogFinding(LogFindingRequest{
			TargetID: "proj-142", Finding: "x", Source: store.Source{Kind: store.SourceFile, Ref: "a@1"},
		})
		if err != nil {
			t.Fatalf("LogFinding: %v", err)
		}
		if entry.Source.Retrieved != "2026-09-19" {
			t.Errorf("retrieved = %q", entry.Source.Retrieved)
		}
	})

	t.Run("confidence is derived, not asserted", func(t *testing.T) {
		entry, err := s.LogFinding(LogFindingRequest{
			TargetID: "proj-142", Finding: "a guess", Confidence: store.ConfidenceHigh,
			Source: store.Source{Kind: store.SourceInferred},
		})
		if err != nil {
			t.Fatalf("LogFinding: %v", err)
		}
		if entry.Confidence != store.ConfidenceLow {
			t.Errorf("confidence = %q, want the derived low despite the request", entry.Confidence)
		}
	})
}

// A Desktop session answering "what do we know about X" must be able to write
// back what it confirmed.
func TestLogFindingAgainstAProject(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	entry, err := s.LogFinding(LogFindingRequest{
		TargetID: "proj", Finding: "there is no staging environment",
		Source: store.Source{Kind: store.SourceConfluence, Ref: "ops page", Retrieved: "2026-09-19"},
	})
	if err != nil {
		t.Fatalf("LogFinding against a project: %v", err)
	}
	if entry.Project != "proj" || entry.Task != "" {
		t.Errorf("entry = %+v, want a project-scoped entry with no task", entry)
	}
	if entry.Scope != store.ScopeProject {
		t.Errorf("scope = %q, want it promoted to project", entry.Scope)
	}
}

func TestCloseTaskRefusesAnEmptySession(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	_, err := s.CloseTask(CloseTaskRequest{TaskID: "proj-142", Summary: "done"})
	if !errors.Is(err, ErrNothingLogged) {
		t.Fatalf("CloseTask returned %v, want ErrNothingLogged", err)
	}
	// The refusal has to say how to get unstuck, or it is just an obstacle.
	if !strings.Contains(err.Error(), "log_decision") {
		t.Errorf("refusal does not say what to do: %v", err)
	}
}

func TestCloseTaskAcceptsAfterSomethingWasLogged(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogDecision(LogDecisionRequest{
		TaskID: "proj-142", Decision: "chose B", Rationale: "cheaper",
	}); err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if _, err := s.CloseTask(CloseTaskRequest{TaskID: "proj-142", Summary: "shipped"}); err != nil {
		t.Fatalf("CloseTask: %v", err)
	}
}

// Re-opening a closed task starts a fresh session: the previous session's
// entries must not satisfy this one's close.
func TestCloseTaskCountsOnlySinceTheLastOpen(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogDecision(LogDecisionRequest{
		TaskID: "proj-142", Decision: "chose B", Rationale: "cheaper",
	}); err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if _, err := s.CloseTask(CloseTaskRequest{TaskID: "proj-142", Summary: "shipped"}); err != nil {
		t.Fatalf("CloseTask: %v", err)
	}

	// A later session opens again and logs nothing.
	s.SetClock(func() time.Time { return now.Add(24 * time.Hour) })
	if _, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"}); err != nil {
		t.Fatalf("re-open: %v", err)
	}
	_, err := s.CloseTask(CloseTaskRequest{TaskID: "proj-142", Summary: "nothing this time"})
	if !errors.Is(err, ErrNothingLogged) {
		t.Fatalf("CloseTask returned %v, want a refusal for the new session", err)
	}
}

func mustOpenTask(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.OpenTask(OpenTaskRequest{
		TaskID: "proj-142",
		Seed:   &Seed{Source: "jira", SourceKey: "PROJ-142", Title: "Example task", Repos: []string{"acme/infrastructure"}},
	}); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
}

// Host detection reads the environment, which over SSE belongs to the menu bar
// app. The MCP client's declared name is the only thing that tells two
// connected agents apart, so it has to win.
func TestSSETransportStampsTheClientAsHost(t *testing.T) {
	s := newService(t, map[string]string{"KEPLER_TASK_ID": "uuid", "CLAUDECODE": "1"})
	openProject(t, s)
	mustOpenTask(t, s)

	s.WithTransport(TransportSSE).WithCallerHost("claude-desktop")

	entry, err := s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "d", Rationale: "r"})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if entry.Host != "claude-desktop" {
		t.Errorf("host = %q, want the connected client rather than the server's environment", entry.Host)
	}

	// With no client name at all, say so rather than claiming to be the host
	// the server process happens to sit in.
	s.WithCallerHost("")
	entry, err = s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "d2", Rationale: "r"})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if entry.Host != "sse-client" {
		t.Errorf("host = %q, want sse-client", entry.Host)
	}
}

// The session id has the same problem as the host: over SSE the environment
// holds the menu bar app's, which would be stamped on every client's entries.
func TestSSETransportStampsTheCallersSession(t *testing.T) {
	s := newService(t, map[string]string{"KEPLER_SESSION_ID": "the-desktop-session"})
	openProject(t, s)
	mustOpenTask(t, s)

	s.WithTransport(TransportSSE).WithCallerSession("mcp-session-42")

	entry, err := s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "d", Rationale: "r"})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if entry.Session != "mcp-session-42" {
		t.Errorf("session = %q, want the caller's", entry.Session)
	}

	// With nothing from the caller, record nothing rather than the server's.
	s.WithCallerSession("")
	entry, err = s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "d2", Rationale: "r"})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	if entry.Session != "" {
		t.Errorf("session = %q, want empty rather than the server's own", entry.Session)
	}
}

// Every verb that writes needs the task named. The inference that used to fill
// this in was removed because a wrong answer is undetectable, so the refusal
// has to be real.
func TestWritingVerbsRequireATaskID(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogDecision(LogDecisionRequest{Decision: "d", Rationale: "r"}); err == nil {
		t.Error("LogDecision accepted a call with no task id")
	}
	if _, err := s.CloseTask(CloseTaskRequest{Summary: "done"}); err == nil {
		t.Error("CloseTask accepted a call with no task id")
	}
	if _, err := s.OpenTask(OpenTaskRequest{Seed: &Seed{Title: "x"}}); err == nil {
		t.Error("OpenTask accepted a call with no task id")
	}
	if _, err := s.LogFinding(LogFindingRequest{
		Finding: "f", Source: store.Source{Kind: store.SourceFile},
	}); err == nil {
		t.Error("LogFinding accepted a call with no target id")
	}
}

func TestLogDependency(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)
	if _, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-139", Seed: &Seed{Title: "The prerequisite"}}); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}

	t.Run("records the ordering with wikilinks", func(t *testing.T) {
		entry, err := s.LogDependency(LogDependencyRequest{
			TaskID: "proj-142", DependsOn: []string{"proj-139"},
			Reason: "The module has to be split first",
		})
		if err != nil {
			t.Fatalf("LogDependency: %v", err)
		}
		// The wikilink is what gives the vault its own graph view for free.
		// Path-qualified, because a bare id can name a tracker export too.
		if !strings.Contains(entry.Body, "[[proj/tasks/proj-139|proj-139]]") {
			t.Errorf("body has no wikilink: %q", entry.Body)
		}
		if !strings.Contains(entry.Body, "split first") {
			t.Errorf("body lost the reason: %q", entry.Body)
		}
	})

	t.Run("requires a reason", func(t *testing.T) {
		_, err := s.LogDependency(LogDependencyRequest{
			TaskID: "proj-142", DependsOn: []string{"proj-139"},
		})
		if err == nil {
			t.Error("accepted an ordering with no justification")
		}
	})

	// A mistyped id would block work that nothing is actually waiting for.
	t.Run("refuses an unknown blocker", func(t *testing.T) {
		_, err := s.LogDependency(LogDependencyRequest{
			TaskID: "proj-142", DependsOn: []string{"proj-999"}, Reason: "typo",
		})
		if err == nil || !strings.Contains(err.Error(), "open_task it first") {
			t.Errorf("LogDependency returned %v, want a refusal naming the fix", err)
		}
	})

	t.Run("refuses a loop", func(t *testing.T) {
		_, err := s.LogDependency(LogDependencyRequest{
			TaskID: "proj-139", DependsOn: []string{"proj-142"}, Reason: "circular",
		})
		if err == nil || !strings.Contains(err.Error(), "loop") {
			t.Errorf("LogDependency returned %v, want a cycle refusal", err)
		}
	})

	t.Run("refuses self-dependency", func(t *testing.T) {
		_, err := s.LogDependency(LogDependencyRequest{
			TaskID: "proj-142", DependsOn: []string{"proj-142"}, Reason: "nonsense",
		})
		if err == nil {
			t.Error("a task was allowed to depend on itself")
		}
	})
}

// The bundle has to warn before it informs: starting blocked work is the
// expensive mistake the ordering exists to prevent.
func TestBundleWarnsWhenBlocked(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)
	if _, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-139", Seed: &Seed{Title: "The prerequisite"}}); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	if _, err := s.LogDependency(LogDependencyRequest{
		TaskID: "proj-142", DependsOn: []string{"proj-139"}, Reason: "order",
	}); err != nil {
		t.Fatalf("LogDependency: %v", err)
	}

	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"})
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	if len(b.OpenBlockers) != 1 {
		t.Fatalf("OpenBlockers = %v, want one", b.OpenBlockers)
	}
	out := b.Render()
	if !strings.Contains(out, "Blocked") || !strings.Contains(out, "proj-139") {
		t.Errorf("the bundle does not lead with the blocker:\n%s", out)
	}
	if strings.Index(out, "Blocked") > strings.Index(out, "## Task") {
		t.Error("the blocker is reported after the task details rather than before")
	}
}
