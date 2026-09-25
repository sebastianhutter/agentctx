package store

import (
	"strings"
	"testing"
	"time"
)

func healthFixture(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 9, 19, 14, 0, 0, 0, time.Local) })
	return s
}

func mustProject(t *testing.T, s *Store, note *ProjectNote) {
	t.Helper()
	if err := s.CreateProject(note); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
}

func mustTask(t *testing.T, s *Store, note *TaskNote) {
	t.Helper()
	if err := s.CreateTask(note); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
}

func mustAppend(t *testing.T, s *Store, e *Entry) {
	t.Helper()
	if e.Host == "" {
		e.Host = "test-host"
	}
	if e.Body == "" {
		e.Body = "something worth knowing"
	}
	if _, err := s.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func issuesWith(issues []Issue, code string) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.Code == code {
			out = append(out, i)
		}
	}
	return out
}

func mustCheck(t *testing.T, s *Store) []Issue {
	t.Helper()
	issues, err := s.Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	return issues
}

func TestCheckCleanVaultReportsNothing(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", JiraProjects: []string{"PROJ"},
		Repos: []string{"acme/infrastructure"}})
	mustTask(t, s, &TaskNote{ID: "proj-142", Project: "proj", Source: "jira", Repos: []string{"acme/infrastructure"}})
	mustAppend(t, s, &Entry{Kind: KindFinding, Task: "proj-142", Project: "proj", Scope: ScopeRepo,
		Repo: "acme/infrastructure", Source: &Source{Kind: SourceFile, Ref: "main.tf@abc1234", Retrieved: "2026-09-19"}})

	if issues := mustCheck(t, s); len(issues) != 0 {
		t.Errorf("clean vault reported %+v", issues)
	}
}

func TestCheckRepoUnlisted(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", JiraProjects: []string{"PROJ"},
		Repos: []string{"acme/infrastructure"}})
	mustTask(t, s, &TaskNote{ID: "proj-142", Project: "proj", Source: "jira", Repos: []string{"acme/platform"}})
	mustAppend(t, s, &Entry{Kind: KindFinding, Task: "proj-142", Project: "proj", Scope: ScopeRepo,
		Repo: "acme/platform", Source: &Source{Kind: SourceInferred, Retrieved: "2026-09-19"}})

	got := issuesWith(mustCheck(t, s), IssueRepoUnlisted)
	if len(got) != 1 || got[0].Subject != "acme/platform" {
		t.Fatalf("repo-unlisted = %+v, want one for acme/platform", got)
	}
	if !strings.Contains(got[0].Message, "task proj-142 and 1 log entry") {
		t.Errorf("message %q should name both the task and the entry", got[0].Message)
	}
}

func TestCheckUnmappedKeys(t *testing.T) {
	s := healthFixture(t)
	// PROJ is claimed by nobody and used only by example: claim it. (In a
	// project whose id were "proj" it would resolve by the id alone.)
	mustProject(t, s, &ProjectNote{ID: "example", Name: "Example"})
	mustTask(t, s, &TaskNote{ID: "proj-142", Project: "example", Source: "jira"})
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Resolves by id"})
	mustTask(t, s, &TaskNote{ID: "proj-143", Project: "proj", Source: "jira"})
	// OTHER is claimed by alpha; beta uses it too and must pass project.
	mustProject(t, s, &ProjectNote{ID: "alpha", Name: "Alpha", JiraProjects: []string{"OTHER"}})
	mustProject(t, s, &ProjectNote{ID: "beta", Name: "Beta"})
	mustTask(t, s, &TaskNote{ID: "other-7", Project: "beta", Source: "jira"})
	// NOPE spans two projects and is claimed by neither.
	mustTask(t, s, &TaskNote{ID: "nope-1", Project: "alpha", Source: "jira"})
	mustTask(t, s, &TaskNote{ID: "nope-2", Project: "beta", Source: "jira"})
	// A manual task with a key-shaped id is not a tracker key.
	mustTask(t, s, &TaskNote{ID: "wip-3", Project: "alpha", Source: "manual"})

	got := map[string]Issue{}
	for _, i := range issuesWith(mustCheck(t, s), IssueKeyUnmapped) {
		got[i.Project+"/"+i.Subject] = i
	}
	if len(got) != 4 {
		t.Fatalf("key-unmapped = %+v, want 4", got)
	}
	if i := got["example/PROJ"]; i.Severity != SeverityWarn || !strings.Contains(i.Fix, "add_tracker_keys=[PROJ]") {
		t.Errorf("an unclaimed single-project key should say to claim it: %+v", i)
	}
	if i := got["beta/OTHER"]; i.Severity != SeverityInfo || !strings.Contains(i.Message, "resolve to alpha") {
		t.Errorf("a key claimed elsewhere should say where it resolves: %+v", i)
	}
	for _, k := range []string{"alpha/NOPE", "beta/NOPE"} {
		if i := got[k]; i.Severity != SeverityInfo || !strings.Contains(i.Fix, "pass project=") {
			t.Errorf("a shared key must not advise claiming it in both projects: %+v", i)
		}
	}
}

func TestCheckKeyClaimedTwice(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "alpha", Name: "Alpha", JiraProjects: []string{"PROJ"}, GitLabGroups: []string{"acme"}})
	mustProject(t, s, &ProjectNote{ID: "beta", Name: "Beta", JiraProjects: []string{"proj"}, GitLabGroups: []string{"acme"}})

	got := map[string]Issue{}
	for _, i := range issuesWith(mustCheck(t, s), IssueKeyClaimedTwice) {
		got[i.Subject] = i
	}
	if got["PROJ"].Severity != SeverityWarn {
		t.Errorf("a tracker key claimed twice is a warning, case-insensitively: %+v", got)
	}
	if got["ACME"].Severity != SeverityInfo {
		t.Errorf("a shared forge group is only info: %+v", got)
	}
}

func TestCheckRepoNearDuplicate(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", Repos: []string{"acme/platform"}})
	mustAppend(t, s, &Entry{Kind: KindFinding, Project: "proj", Scope: ScopeRepo, Repo: "platform",
		Source: &Source{Kind: SourceInferred, Retrieved: "2026-09-19"}})

	got := issuesWith(mustCheck(t, s), IssueRepoNearDuplicate)
	if len(got) != 1 || got[0].Subject != "platform" || !strings.Contains(got[0].Fix, "acme/platform") {
		t.Errorf("repo-near-duplicate = %+v", got)
	}
}

func TestCheckOrphansAndDependencies(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", JiraProjects: []string{"PROJ"}})
	mustTask(t, s, &TaskNote{ID: "proj-1", Project: "proj", Source: "jira"})
	mustAppend(t, s, &Entry{Kind: KindDecision, Task: "proj-1", Project: "proj", Scope: ScopeTask})
	mustAppend(t, s, &Entry{Kind: KindDecision, Task: "proj-9", Project: "proj", Scope: ScopeTask})
	mustAppend(t, s, &Entry{Kind: KindDependency, Task: "proj-1", Project: "proj", Scope: ScopeTask,
		DependsOn: []string{"nope-1"}})

	issues := mustCheck(t, s)
	if got := issuesWith(issues, IssueEntryOrphan); len(got) != 1 || got[0].Subject != "proj-9" {
		t.Errorf("entry-orphan = %+v", got)
	}
	if got := issuesWith(issues, IssueDependencyUnknown); len(got) != 1 || !strings.Contains(got[0].Message, "nope-1") {
		t.Errorf("dependency-unknown = %+v", got)
	}
}

func TestCheckNothingLoggedAndBacklog(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", JiraProjects: []string{"PROJ"}})
	mustTask(t, s, &TaskNote{ID: "proj-1", Project: "proj", Source: "jira"})
	mustTask(t, s, &TaskNote{ID: "proj-2", Project: "proj", Source: "jira"})
	mustAppend(t, s, &Entry{Kind: KindOpened, Task: "proj-1", Project: "proj", Scope: ScopeTask})
	for i := 0; i < BacklogThreshold; i++ {
		mustAppend(t, s, &Entry{Kind: KindDecision, Task: "proj-2", Project: "proj", Scope: ScopeTask})
	}

	issues := mustCheck(t, s)
	if got := issuesWith(issues, IssueTaskNothingLogged); len(got) != 1 || got[0].Subject != "proj-1" {
		t.Errorf("task-nothing-logged = %+v, want proj-1 only (an opened entry is not logging)", got)
	}
	if got := issuesWith(issues, IssueBacklog); len(got) != 1 || got[0].Subject != "proj-2" {
		t.Errorf("consolidation-backlog = %+v, want proj-2", got)
	}
}

// A repo logged under the wrong name stops being reported once the entry is
// replaced: the check reads the live log, not the history.
func TestCheckIgnoresSupersededEntries(t *testing.T) {
	s := healthFixture(t)
	mustProject(t, s, &ProjectNote{ID: "proj", Name: "Example", Repos: []string{"acme/platform"}})
	wrong := &Entry{Kind: KindFinding, Project: "proj", Scope: ScopeRepo, Repo: "platform",
		Source: &Source{Kind: SourceInferred, Retrieved: "2026-09-19"}}
	mustAppend(t, s, wrong)
	if len(issuesWith(mustCheck(t, s), IssueRepoUnlisted)) == 0 {
		t.Fatal("fixture: the bare repo name should be reported first")
	}

	mustAppend(t, s, &Entry{Kind: KindFinding, Project: "proj", Scope: ScopeRepo, Repo: "acme/platform",
		Source:     &Source{Kind: SourceInferred, Retrieved: "2026-09-19"},
		Supersedes: []string{wrong.ID()}})

	issues := mustCheck(t, s)
	for _, code := range []string{IssueRepoUnlisted, IssueRepoNearDuplicate} {
		if got := issuesWith(issues, code); len(got) != 0 {
			t.Errorf("%s still reported after the entry was replaced: %+v", code, got)
		}
	}
}
