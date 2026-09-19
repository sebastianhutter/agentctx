package bundle

import (
	"strings"
	"testing"
	"time"

	"github.com/sebastianhutter/agentctx/internal/store"
)

var now = time.Date(2026, 9, 19, 14, 32, 7, 0, time.Local)

func setup(t *testing.T) (*store.Store, *Assembler) {
	t.Helper()
	s := store.New(t.TempDir())
	s.SetClock(func() time.Time { return now })

	if err := s.CreateProject(&store.ProjectNote{
		ID: "proj", Name: "Example project", JiraProjects: []string{"PROJ"},
		Repos: []string{"acme/infrastructure", "acme/platform"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateTask(&store.TaskNote{
		ID: "proj-142", Project: "proj", Source: "jira", SourceKey: "PROJ-142",
		Title: "Example task", Repos: []string{"acme/infrastructure"},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	a := New(s, 30)
	a.Now = func() time.Time { return now }
	return s, a
}

func TestTaskBundleCollectsEveryScope(t *testing.T) {
	s, a := setup(t)

	append := func(e *store.Entry) {
		t.Helper()
		if _, err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	append(&store.Entry{Kind: store.KindDecision, Task: "proj-142", Project: "proj", Scope: store.ScopeTask,
		TS: "2026-09-19T14:00:00", Body: "chose option B"})
	append(&store.Entry{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeProject,
		TS: "2026-09-19T14:01:00", Body: "no staging environment",
		Source: &store.Source{Kind: store.SourceConfluence, Ref: "page 12", Retrieved: "2026-09-18"}})
	append(&store.Entry{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeRepo, Repo: "acme/infrastructure",
		TS: "2026-09-19T14:02:00", Body: "module breaks on upgrade",
		Source: &store.Source{Kind: store.SourceFile, Ref: "modules/vpc/main.tf@abc1234", Retrieved: "2026-09-19"}})
	// A finding about a repo this task does not touch must not appear.
	append(&store.Entry{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeRepo, Repo: "acme/platform",
		TS: "2026-09-19T14:03:00", Body: "unrelated repo fact",
		Source: &store.Source{Kind: store.SourceFile, Ref: "x@1", Retrieved: "2026-09-19"}})

	b, err := a.Task("proj", "proj-142")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(b.Unconsolidated) != 1 {
		t.Errorf("unconsolidated = %d, want 1", len(b.Unconsolidated))
	}
	if len(b.ProjectFindings) != 1 {
		t.Errorf("project findings = %d, want 1", len(b.ProjectFindings))
	}
	if len(b.RepoFindings) != 1 || len(b.RepoFindings["acme/infrastructure"]) != 1 {
		t.Errorf("repo findings = %v, want only the task's repo", b.RepoFindings)
	}

	out := b.Render()
	for _, want := range []string{"chose option B", "no staging environment", "module breaks on upgrade", "Working agreement"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered bundle is missing %q", want)
		}
	}
	if strings.Contains(out, "unrelated repo fact") {
		t.Error("bundle leaked a finding about a repo the task does not touch")
	}
}

// consolidated_through is compaction, never correctness: entries newer than it
// must still be returned, or folding the log would lose facts.
func TestTaskBundleRespectsConsolidatedThrough(t *testing.T) {
	s, a := setup(t)

	for _, e := range []*store.Entry{
		{Kind: store.KindDecision, Task: "proj-142", Project: "proj", TS: "2026-09-19T10:00:00", Body: "old, already folded in"},
		{Kind: store.KindDecision, Task: "proj-142", Project: "proj", TS: "2026-09-19T14:00:00", Body: "new, not yet folded in"},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	note, _, err := s.ReadTask("proj", "proj-142")
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	note.ConsolidatedThrough = "2026-09-19T12:00:00"
	rewrite(t, s, note)

	b, err := a.Task("proj", "proj-142")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if len(b.Unconsolidated) != 1 || b.Unconsolidated[0].Entry.Body != "new, not yet folded in" {
		t.Errorf("unconsolidated = %+v, want only the newer entry", b.Unconsolidated)
	}
}

func TestStalenessMarking(t *testing.T) {
	s, a := setup(t)

	for _, e := range []*store.Entry{
		{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeProject, TS: "2026-09-19T14:00:00",
			Body: "fresh", Source: &store.Source{Kind: store.SourceConfluence, Retrieved: "2026-09-18"}},
		{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeProject, TS: "2026-09-19T14:01:00",
			Body: "old", Source: &store.Source{Kind: store.SourceConfluence, Retrieved: "2026-06-01"}},
		{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeProject, TS: "2026-09-19T14:02:00",
			Body: "guessed", Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}},
		{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeProject, TS: "2026-09-19T14:03:00",
			Body: "bad date", Source: &store.Source{Kind: store.SourceFile, Retrieved: "last tuesday"}},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	b, err := a.Project("proj")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(b.Findings) != 4 {
		t.Fatalf("findings = %d, want 4", len(b.Findings))
	}

	byBody := map[string]Marked{}
	for _, m := range b.Findings {
		byBody[m.Entry.Body] = m
	}
	if byBody["fresh"].Stale {
		t.Error("a finding retrieved yesterday was marked stale")
	}
	if !byBody["old"].Stale || !strings.Contains(byBody["old"].Reason, "days ago") {
		t.Errorf("an old finding was not marked: %+v", byBody["old"])
	}
	// An inference is never evidence, however recent.
	if !byBody["guessed"].Stale || !strings.Contains(byBody["guessed"].Reason, "not evidence") {
		t.Errorf("an inferred finding was not marked: %+v", byBody["guessed"])
	}
	if !byBody["bad date"].Stale {
		t.Error("a finding with an unparseable date was not marked")
	}

	if out := b.Render(); !strings.Contains(out, "re-verify") {
		t.Error("rendered project bundle does not surface the re-verify markers")
	}
}

func TestProjectBundleOpenTasksDerivedFromLog(t *testing.T) {
	s, a := setup(t)

	if err := s.CreateTask(&store.TaskNote{ID: "proj-143", Project: "proj"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.Append(&store.Entry{
		Kind: store.KindClosed, Task: "proj-143", Project: "proj", TS: "2026-09-19T14:00:00", Body: "done",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	b, err := a.Project("proj")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if len(b.OpenTasks) != 1 || b.OpenTasks[0] != "proj-142" {
		t.Errorf("open tasks = %v, want [proj-142]", b.OpenTasks)
	}
}

// The cold start has to be honest rather than encouraging.
func TestEmptyBundleSaysSo(t *testing.T) {
	_, a := setup(t)

	b, err := a.Task("proj", "proj-142")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	out := b.Render()
	if !strings.Contains(out, "Nothing recorded yet") {
		t.Errorf("empty bundle does not say it is empty:\n%s", out)
	}
	// A new note is not the same claim as an empty vault: the header must not
	// deny knowledge that the sections below go on to print.
	if strings.Contains(out, "No knowledge recorded yet") {
		t.Error("the created-note header overstates what is unknown")
	}
	// And the four empty headings from the fresh note are not dressed up as content.
	if strings.Contains(out, "## Open threads") {
		t.Error("empty note sections were rendered as though they held something")
	}
}

func TestWorkingAgreementNamesTheScopeRule(t *testing.T) {
	// The agreement is the only instruction channel every host reads the same
	// way, and scope is the decision it exists to influence.
	for _, want := range []string{"scope: repo", "scope: project", "provenance", "close_task"} {
		if !strings.Contains(WorkingAgreement, want) {
			t.Errorf("working agreement does not mention %q", want)
		}
	}
}

func rewrite(t *testing.T, s *store.Store, note *store.TaskNote) {
	t.Helper()
	path := s.TaskPath(note.Project, note.ID)
	if err := removeAndRecreate(path, note); err != nil {
		t.Fatalf("rewrite task note: %v", err)
	}
}
