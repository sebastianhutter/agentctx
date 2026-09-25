package service

import (
	"os"
	"strings"
	"testing"

	"github.com/sebastianhutter/agentctx/internal/store"
)

func openTask(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, err := s.OpenTask(OpenTaskRequest{TaskID: id, Seed: &Seed{Source: "jira", SourceKey: strings.ToUpper(id), Title: id}}); err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
}

func logRepoFinding(t *testing.T, s *Service, target, repo, text string, supersedes ...string) *store.Entry {
	t.Helper()
	e, err := s.LogFinding(LogFindingRequest{
		TargetID: target, Finding: text, Scope: store.ScopeRepo, Repo: repo,
		Source:     store.Source{Kind: store.SourceFile, Ref: "main.tf@abc1234"},
		Supersedes: supersedes,
	})
	if err != nil {
		t.Fatalf("LogFinding: %v", err)
	}
	return e
}

// The case supersedes exists for: a finding filed under the wrong repo name.
// Re-filing it must take the old one out of every bundle, while the old file
// stays exactly as it was.
func TestSupersedeRefilesAFinding(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	openTask(t, s, "proj-142")

	wrong := logRepoFinding(t, s, "proj-142", "infrastructure", "The module breaks on upgrade")
	before, err := os.ReadFile(wrong.Path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	right := logRepoFinding(t, s, "proj-142", "acme/infrastructure", "The module breaks on upgrade", wrong.ID())

	if got := right.Supersedes; len(got) != 1 || got[0] != wrong.ID() {
		t.Fatalf("Supersedes = %v, want [%s]", got, wrong.ID())
	}

	live, err := s.Store.Entries("proj", store.EntryFilter{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	for _, e := range live {
		if e.ID() == wrong.ID() {
			t.Error("a superseded entry is still returned by default")
		}
	}

	all, err := s.Store.Entries("proj", store.EntryFilter{IncludeSuperseded: true})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	found := false
	for _, e := range all {
		if e.ID() == wrong.ID() {
			found = true
			if e.SupersededBy != right.ID() {
				t.Errorf("SupersededBy = %q, want %q", e.SupersededBy, right.ID())
			}
		}
	}
	if !found {
		t.Error("IncludeSuperseded did not return the replaced entry")
	}

	after, _ := os.ReadFile(wrong.Path)
	if string(after) != string(before) {
		t.Error("superseding edited the old entry; the log is append-only")
	}

	b, err := s.Assembly.Task("proj", "proj-142")
	if err != nil {
		t.Fatalf("Task bundle: %v", err)
	}
	out := b.Render()
	if strings.Contains(out, wrong.ID()) {
		t.Error("the task bundle still shows the replaced entry")
	}
	if !strings.Contains(out, "id "+right.ID()) {
		t.Error("the bundle should show each entry's id, which is what supersedes takes")
	}
}

// A replacement may come from another task, or from the project itself; the
// entry it replaces disappears from its own task's view either way.
func TestSupersedeAcrossTasks(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	openTask(t, s, "proj-1")
	openTask(t, s, "proj-2")

	old := logRepoFinding(t, s, "proj-1", "acme/infrastructure", "First take")
	logRepoFinding(t, s, "proj", "acme/infrastructure", "Corrected", old.ID())

	entries, err := s.Store.Entries("proj", store.EntryFilter{Task: "proj-1", Kinds: []string{store.KindFinding}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("proj-1 still shows %d findings after its only one was replaced from the project", len(entries))
	}
}

func TestSupersedeRefusals(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	openTask(t, s, "proj-142")
	if _, err := s.OpenProject(OpenProjectRequest{ID: "other", Name: "Other", JiraProjects: []string{"OTHER"}}); err != nil {
		t.Fatalf("OpenProject: %v", err)
	}

	first := logRepoFinding(t, s, "proj-142", "acme/infrastructure", "First")
	logRepoFinding(t, s, "proj-142", "acme/infrastructure", "Second", first.ID())

	opened, err := s.Store.Entries("proj", store.EntryFilter{Kinds: []string{store.KindOpened}})
	if err != nil || len(opened) == 0 {
		t.Fatalf("no opened entry: %v", err)
	}
	foreign, err := s.LogFinding(LogFindingRequest{TargetID: "other", Finding: "Elsewhere",
		Source: store.Source{Kind: store.SourceInferred}})
	if err != nil {
		t.Fatalf("LogFinding: %v", err)
	}

	for _, tc := range []struct{ name, ref, want string }{
		{"unknown", "2026-01-01T0000-nope-finding-0000", "no such entry"},
		{"already replaced", first.ID(), "already replaced by"},
		{"not supersedable", opened[0].ID(), "cannot be replaced"},
		{"another project", foreign.ID(), "no such entry"},
	} {
		_, err := s.LogFinding(LogFindingRequest{TargetID: "proj-142", Finding: "x",
			Source: store.Source{Kind: store.SourceInferred}, Supersedes: []string{tc.ref}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestSupersedeAcceptsAFileNameAndADecision(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	openTask(t, s, "proj-142")

	d, err := s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "Chose A", Rationale: "because"})
	if err != nil {
		t.Fatalf("LogDecision: %v", err)
	}
	// The path log_decision printed, file name and all, is accepted as well.
	d2, err := s.LogDecision(LogDecisionRequest{TaskID: "proj-142", Decision: "Chose B after all",
		Rationale: "A failed", Supersedes: []string{d.Path}})
	if err != nil {
		t.Fatalf("LogDecision superseding: %v", err)
	}
	if len(d2.Supersedes) != 1 || d2.Supersedes[0] != d.ID() {
		t.Errorf("Supersedes = %v, want the bare id %s", d2.Supersedes, d.ID())
	}
}
