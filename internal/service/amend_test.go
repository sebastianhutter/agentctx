package service

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// newProject creates a project claiming no keys, as projects often start.
func newProject(t *testing.T, s *Service, id string, repos ...string) {
	t.Helper()
	if _, err := s.OpenProject(OpenProjectRequest{ID: id, Name: id, Repos: repos}); err != nil {
		t.Fatalf("OpenProject %s: %v", id, err)
	}
}

// openIn opens a tracker task in an explicitly named project.
func openIn(t *testing.T, s *Service, project, id string, repos ...string) string {
	t.Helper()
	b, err := s.OpenTask(OpenTaskRequest{TaskID: id, Project: project,
		Seed: &Seed{Source: "jira", SourceKey: strings.ToUpper(id), Title: id, Repos: repos}})
	if err != nil {
		t.Fatalf("OpenTask %s: %v", id, err)
	}
	return b.Render()
}

func amend(s *Service, req AmendProjectRequest) error {
	if req.Reason == "" {
		req.Reason = "seen in the work"
	}
	_, err := s.AmendProject(req)
	return err
}

func TestAmendProjectRepos(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha", "acme/platform")
	before, err := os.ReadFile(s.Store.ProjectPath("alpha"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", AddRepos: []string{"acme/infrastructure"}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	note, _, _ := s.Store.ReadProject("alpha")
	if len(note.Repos) != 2 || note.Repos[1] != "acme/infrastructure" {
		t.Errorf("repos after add = %v", note.Repos)
	}
	after, _ := os.ReadFile(s.Store.ProjectPath("alpha"))
	if string(after) != string(before) {
		t.Error("an amendment rewrote the project note; it should only append to the log")
	}

	s.SetClock(func() time.Time { return now.Add(time.Minute) })
	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", RemoveRepos: []string{"acme/platform"}}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	note, _, _ = s.Store.ReadProject("alpha")
	if len(note.Repos) != 1 || note.Repos[0] != "acme/infrastructure" {
		t.Errorf("repos after remove = %v", note.Repos)
	}

	for name, req := range map[string]AmendProjectRequest{
		"bare name beside a qualified one": {ProjectID: "alpha", AddRepos: []string{"infrastructure"}},
		"removing a repo it does not have": {ProjectID: "alpha", RemoveRepos: []string{"acme/nothing"}},
		"nothing to change":                {ProjectID: "alpha", AddRepos: []string{"acme/infrastructure"}},
		"no reason":                        {ProjectID: "alpha", AddRepos: []string{"acme/x"}, Reason: " "},
	} {
		if req.Reason == " " {
			if _, err := s.AmendProject(req); err == nil {
				t.Errorf("%s: accepted", name)
			}
			continue
		}
		if err := amend(s, req); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The case that asked for this: a project whose tasks come from a tracker it
// never claimed. Once claimed, open_task resolves a new key by itself.
func TestAmendProjectClaimsAKey(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha")
	openIn(t, s, "alpha", "proj-1")

	if _, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-2", Seed: &Seed{Source: "jira", SourceKey: "PROJ-2", Title: "t"}}); err == nil {
		t.Fatal("fixture: an unclaimed key should not resolve yet")
	}
	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", AddKeys: []string{"proj"}}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-2", Seed: &Seed{Source: "jira", SourceKey: "PROJ-2", Title: "t"}})
	if err != nil {
		t.Fatalf("a claimed key should resolve: %v", err)
	}
	if b.Task.Project != "alpha" {
		t.Errorf("proj-2 went to %s", b.Task.Project)
	}

	// Timestamps have second resolution and the test clock is fixed: a
	// release written in the same second as its claim has no defined order.
	s.SetClock(func() time.Time { return now.Add(time.Minute) })
	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", RemoveKeys: []string{"PROJ"}}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := s.Store.ResolveProjectForKey("PROJ-3"); err == nil {
		t.Error("a released key still resolves")
	}
}

func TestAmendProjectRefusesUnsafeKeys(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha")
	newProject(t, s, "beta")
	if _, err := s.OpenProject(OpenProjectRequest{ID: "owner", Name: "owner", JiraProjects: []string{"OTHER"}}); err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	openIn(t, s, "alpha", "other-7")
	// NOPE is a tracker both alpha and beta take tasks from.
	openIn(t, s, "alpha", "nope-1")
	openIn(t, s, "beta", "nope-2")

	for _, tc := range []struct{ key, want string }{
		{"OTHER", "is claimed by owner"},
		{"NOPE", "shared between projects"},
		{"UNUSED", "no task in project alpha uses"},
		{"BETA", "resolves to project beta by its id"},
		{"PROJ-1", "prefix alone"},
	} {
		err := amend(s, AmendProjectRequest{ProjectID: "alpha", AddKeys: []string{tc.key}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.key, err, tc.want)
		}
	}
}

// An amendment is an entry like any other, so superseding it undoes it.
func TestSupersedingAnAmendmentUndoesIt(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha")
	e, err := s.AmendProject(AmendProjectRequest{ProjectID: "alpha", AddRepos: []string{"acme/wrong"}, Reason: "guess"})
	if err != nil {
		t.Fatalf("AmendProject: %v", err)
	}
	if _, err := s.LogFinding(LogFindingRequest{TargetID: "alpha", Finding: "acme/wrong is unrelated",
		Source: store.Source{Kind: store.SourceConversation}, Supersedes: []string{e.ID()}}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	note, _, _ := s.Store.ReadProject("alpha")
	if len(note.Repos) != 0 {
		t.Errorf("repos = %v after the amendment was superseded", note.Repos)
	}
}

// Consolidation rewrites the note file; it must not bake the folded lists in,
// or a later removal could no longer take effect.
func TestConsolidateDoesNotStoreAmendments(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha")
	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", AddRepos: []string{"acme/platform"}}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	if err := s.Store.ConsolidateProject("alpha", "# alpha\n\nWhat we know.\n", "2026-09-19T14:32:07", "", false); err != nil {
		t.Fatalf("ConsolidateProject: %v", err)
	}
	raw, _ := os.ReadFile(s.Store.ProjectPath("alpha"))
	if strings.Contains(string(raw), "acme/platform") {
		t.Error("consolidation wrote an amended repo into the note's frontmatter")
	}
	if note, _, _ := s.Store.ReadProject("alpha"); len(note.Repos) != 1 {
		t.Errorf("amended repo lost after consolidation: %v", note.Repos)
	}
}

func TestOpenTaskReportsProjectGaps(t *testing.T) {
	s := newService(t, nil)
	newProject(t, s, "alpha", "acme/platform")
	newProject(t, s, "beta")

	out := openIn(t, s, "alpha", "proj-1", "acme/platform", "acme/tools")
	for _, want := range []string{"## Project gaps", `add_repos=["acme/tools"]`, `add_tracker_keys=["PROJ"]`} {
		if !strings.Contains(out, want) {
			t.Errorf("bundle does not contain %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"acme/platform"]`) {
		t.Error("a repo the project lists was reported as a gap")
	}

	// Once the key is shared with another project it is no longer a gap to
	// close by claiming: passing project is how such a tracker works.
	openIn(t, s, "beta", "proj-2")
	if out := openIn(t, s, "alpha", "proj-1"); strings.Contains(out, "add_tracker_keys") {
		t.Error("a shared key was suggested for claiming")
	}

	if err := amend(s, AmendProjectRequest{ProjectID: "alpha", AddRepos: []string{"acme/tools"}}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	if out := openIn(t, s, "alpha", "proj-1"); strings.Contains(out, "## Project gaps") {
		t.Errorf("gaps still reported after they were closed:\n%s", out)
	}
}
