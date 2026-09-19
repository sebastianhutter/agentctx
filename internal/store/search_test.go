package store

import (
	"strings"
	"testing"
	"time"
)

func searchFixture(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 9, 19, 14, 32, 7, 0, time.Local) })

	if err := s.CreateProject(&ProjectNote{
		ID: "proj", Name: "Example project", JiraProjects: []string{"PROJ"},
		Repos: []string{"acme/infrastructure", "acme/platform"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateProject(&ProjectNote{
		ID: "other", Name: "Unrelated", JiraProjects: []string{"OTHER"},
		Repos: []string{"acme/website"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	for _, task := range []*TaskNote{
		{ID: "proj-142", Project: "proj", Title: "Rework the network module",
			SourceKey: "PROJ-142", Repos: []string{"acme/infrastructure"}},
		{ID: "proj-188", Project: "proj", Title: "Add a second VPC",
			SourceKey: "PROJ-188", Repos: []string{"acme/infrastructure"}},
		{ID: "other-7", Project: "other", Title: "Redesign the landing page",
			SourceKey: "OTHER-7", Repos: []string{"acme/website"}},
	} {
		if err := s.CreateTask(task); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}

	for _, e := range []*Entry{
		// Boilerplate this tool writes itself, containing a host name.
		{Kind: KindOpened, Task: "proj-142", Project: "proj", TS: "2026-09-19T10:00:00",
			Host: "kepler-claude-code", Body: "Task created from PROJ-142 on kepler-claude-code."},
		{Kind: KindFinding, Task: "proj-142", Project: "proj", Scope: ScopeRepo, Repo: "acme/infrastructure",
			TS: "2026-09-19T11:00:00", Body: "for_each over a provider-versioned map breaks on upgrade"},
		{Kind: KindFinding, Project: "proj", Scope: ScopeProject, TS: "2026-09-19T12:00:00",
			Body: "There is no staging environment"},
		{Kind: KindClosed, Task: "proj-188", Project: "proj", TS: "2026-09-19T13:00:00",
			Body: "Shipped the second VPC"},
	} {
		if _, err := s.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	return s
}

func ids(matches []Match) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.Kind+":"+m.ID)
	}
	return out
}

// With no query this is the "what is here at all" call, which is how an agent
// that was handed no id finds out whether this work has a project.
func TestSearchWithNoTermsListsEverythingOpen(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	want := map[string]bool{
		"project:proj": true, "project:other": true,
		"task:proj-142": true, "task:other-7": true,
	}
	for _, id := range ids(got) {
		if !want[id] {
			t.Errorf("unexpected result %q", id)
		}
		delete(want, id)
	}
	for id := range want {
		t.Errorf("missing result %q", id)
	}
	// proj-188 is closed, and a closed task is rarely what you want to log against.
	for _, m := range got {
		if m.ID == "proj-188" {
			t.Error("a closed task was listed without include_closed")
		}
	}
}

func TestSearchIncludeClosed(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{IncludeClosed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, m := range got {
		if m.ID == "proj-188" {
			found = true
			if m.Open {
				t.Error("a closed task is reported as open")
			}
		}
	}
	if !found {
		t.Error("include_closed did not surface the closed task")
	}
}

func TestSearchByTermsAcrossIdentity(t *testing.T) {
	s := searchFixture(t)

	for _, tc := range []struct {
		name  string
		terms []string
		want  string
	}{
		{"by title word", []string{"VPC"}, "task:proj-188"},
		{"by tracker key", []string{"proj-142"}, "task:proj-142"},
		{"by project name", []string{"unrelated"}, "project:other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Search(SearchFilter{Terms: tc.terms, IncludeClosed: true})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			for _, id := range ids(got) {
				if id == tc.want {
					return
				}
			}
			t.Errorf("Search(%v) = %v, want it to include %q", tc.terms, ids(got), tc.want)
		})
	}
}

// The case that gives this reach: a query describing a problem finds the task
// where it was recorded, without the search returning the finding itself.
func TestSearchMatchesLoggedText(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{Terms: []string{"provider-versioned"}, IncludeClosed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 || got[0].ID != "proj-142" {
		t.Fatalf("Search = %v, want the task holding that finding", ids(got))
	}
	if !strings.Contains(got[0].Why, "finding") {
		t.Errorf("Why = %q, want it to say a finding matched", got[0].Why)
	}
	if !strings.Contains(got[0].Snippet, "for_each") {
		t.Errorf("Snippet = %q, want one line of the finding", got[0].Snippet)
	}
}

// Opened entries are boilerplate written by this tool and carry the host name.
// Searching them would make every task match its own host.
func TestSearchIgnoresOpenedBoilerplate(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{Terms: []string{"kepler-claude-code"}, IncludeClosed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("searching a host name matched %v", ids(got))
	}
}

// All terms must match, or a two-word query is no narrower than a one-word one.
func TestSearchRequiresEveryTerm(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{Terms: []string{"VPC", "landing"}, IncludeClosed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("terms from two different tasks matched %v", ids(got))
	}
}

func TestSearchByRepo(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{Repo: "acme/website", IncludeClosed: true})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for _, id := range ids(got) {
		if id != "project:other" && id != "task:other-7" {
			t.Errorf("repo filter returned %q, which does not touch that repository", id)
		}
	}
	if len(got) != 2 {
		t.Errorf("got %v, want the project and task for that repo", ids(got))
	}
}

func TestSearchLimit(t *testing.T) {
	s := searchFixture(t)

	got, err := s.Search(SearchFilter{IncludeClosed: true, Limit: 2})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d results, want the limit of 2", len(got))
	}
	// Projects sort first: they are the coarser, safer thing to open.
	if got[0].Kind != "project" {
		t.Errorf("first result is a %s, want a project", got[0].Kind)
	}
}
