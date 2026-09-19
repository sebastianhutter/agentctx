package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 9, 19, 14, 32, 7, 0, time.Local) })
	return s
}

func withProject(t *testing.T, s *Store, id string, note *ProjectNote) {
	t.Helper()
	if note == nil {
		note = &ProjectNote{ID: id, Name: "Example project", JiraProjects: []string{strings.ToUpper(id)}}
	}
	if err := s.CreateProject(note); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
}

func TestCreateProjectWritesNoteAndFolders(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	note, body, err := s.ReadProject("proj")
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	if note.ID != "proj" || note.Created != "2026-09-19" {
		t.Errorf("note = %+v", note)
	}
	for _, section := range []string{"## What we know", "## Notes", "## Tasks"} {
		if !strings.Contains(body, section) {
			t.Errorf("body is missing %q:\n%s", section, body)
		}
	}
	for _, dir := range []string{"log", "notes", "tasks"} {
		if info, err := os.Stat(filepath.Join(s.ProjectDir("proj"), dir)); err != nil || !info.IsDir() {
			t.Errorf("%s/ was not created: %v", dir, err)
		}
	}
}

func TestCreateProjectRefusesDuplicate(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)
	if err := s.CreateProject(&ProjectNote{ID: "proj"}); !errors.Is(err, ErrProjectExists) {
		t.Fatalf("second CreateProject returned %v, want ErrProjectExists", err)
	}
}

func TestListProjectsIgnoresStrayDirectories(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)
	mkdir(t, filepath.Join(s.Root(), ".obsidian"))
	mkdir(t, filepath.Join(s.Root(), "Attachments"))

	got, err := s.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(got) != 1 || got[0] != "proj" {
		t.Errorf("ListProjects = %v, want [proj]", got)
	}
}

func TestResolveProjectForKey(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", &ProjectNote{
		ID: "proj", Name: "Example", JiraProjects: []string{"PROJ", "OTHER"},
		GitLabGroups: []string{"acme"},
	})

	for _, tc := range []struct{ key, want string }{
		{"PROJ-142", "proj"},
		{"proj-142", "proj"},
		{"OTHER-7", "proj"},
		{"acme-3", "proj"},
		{"PROJ", "proj"},
	} {
		got, err := s.ResolveProjectForKey(tc.key)
		if err != nil || got != tc.want {
			t.Errorf("ResolveProjectForKey(%q) = %q, %v; want %q", tc.key, got, err, tc.want)
		}
	}

	// An unknown prefix must refuse rather than guess: a wrong guess files the
	// work under the wrong corpus and nobody notices.
	if _, err := s.ResolveProjectForKey("NOPE-1"); !errors.Is(err, ErrNoProject) {
		t.Errorf("unknown prefix returned %v, want ErrNoProject", err)
	}
	if _, err := s.ResolveProjectForKey(""); !errors.Is(err, ErrNoProject) {
		t.Errorf("empty key returned %v, want ErrNoProject", err)
	}
}

func TestCreateAndReadTask(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	err := s.CreateTask(&TaskNote{
		ID: "proj-142", Project: "proj", Source: "jira", SourceKey: "PROJ-142",
		Title: "Example task", Repos: []string{"acme/infrastructure"},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	note, body, err := s.ReadTask("proj", "proj-142")
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	if note.SourceKey != "PROJ-142" || note.Created != "2026-09-19" || len(note.Repos) != 1 {
		t.Errorf("note = %+v", note)
	}
	for _, section := range []string{"## Current understanding", "## Decisions", "## Findings", "## Open threads"} {
		if !strings.Contains(body, section) {
			t.Errorf("body is missing %q", section)
		}
	}

	if _, _, err := s.ReadTask("proj", "proj-999"); !errors.Is(err, ErrNoTask) {
		t.Errorf("reading a missing task returned %v, want ErrNoTask", err)
	}
}

func TestAppendAndQueryEntries(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	entries := []*Entry{
		{Kind: KindOpened, Task: "proj-142", Project: "proj", Scope: ScopeTask, TS: "2026-09-19T14:00:00", Host: "kepler-claude-code"},
		{Kind: KindDecision, Task: "proj-142", Project: "proj", Scope: ScopeTask, TS: "2026-09-19T14:10:00", Body: "chose option B"},
		{Kind: KindFinding, Task: "proj-142", Project: "proj", Scope: ScopeRepo, Repo: "acme/infrastructure", TS: "2026-09-19T14:20:00", Body: "module breaks on upgrade"},
		{Kind: KindFinding, Project: "proj", Scope: ScopeProject, TS: "2026-09-19T14:30:00", Body: "no staging environment"},
	}
	for _, e := range entries {
		if _, err := s.Append(e); err != nil {
			t.Fatalf("Append(%s): %v", e.Kind, err)
		}
	}

	all, err := s.Entries("proj", EntryFilter{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("got %d entries, want 4", len(all))
	}
	// Oldest first, so a consolidation pass reads them in the order they happened.
	for i := 1; i < len(all); i++ {
		if all[i].Time().Before(all[i-1].Time()) {
			t.Errorf("entries out of order at %d", i)
		}
	}

	repoScoped, err := s.Entries("proj", EntryFilter{Scope: ScopeRepo, Repos: []string{"acme/infrastructure"}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(repoScoped) != 1 || repoScoped[0].Body != "module breaks on upgrade" {
		t.Errorf("repo-scoped query returned %d entries", len(repoScoped))
	}

	// A repo-scoped query must not leak another repo's findings.
	otherRepo, err := s.Entries("proj", EntryFilter{Scope: ScopeRepo, Repos: []string{"acme/platform"}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(otherRepo) != 0 {
		t.Errorf("query for an unrelated repo returned %d entries", len(otherRepo))
	}

	since, _ := ParseTime("2026-09-19T14:15:00")
	recent, err := s.Entries("proj", EntryFilter{Since: since})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(recent) != 2 {
		t.Errorf("since-filter returned %d entries, want 2", len(recent))
	}
}

func TestAppendRequiresRepoForRepoScope(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	_, err := s.Append(&Entry{Kind: KindFinding, Project: "proj", Scope: ScopeRepo, Body: "x"})
	if err == nil || !strings.Contains(err.Error(), "no repo") {
		t.Fatalf("Append returned %v, want a missing-repo error", err)
	}
}

// The design's core claim. Parallel sessions are normal and the vault has no
// locking, so concurrent appends must produce one file each.
func TestAppendConcurrentWritersEachGetAFile(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	const writers = 24
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Same task, same second: the worst case for filename collisions.
			_, errs[i] = s.Append(&Entry{
				Kind: KindFinding, Task: "proj-142", Project: "proj", Scope: ScopeTask,
				TS: "2026-09-19T14:32:07", Body: "concurrent",
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	got, err := s.Entries("proj", EntryFilter{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(got) != writers {
		t.Errorf("got %d entries, want %d — an append was lost", len(got), writers)
	}
}

// A file someone hand-edited into an unparseable state must cost only itself.
func TestEntriesSkipsUnreadableFile(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	if _, err := s.Append(&Entry{Kind: KindDecision, Task: "proj-142", Project: "proj", Body: "good"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	broken := filepath.Join(s.LogDir("proj"), "2026-09-19T1432-proj-142-decision-ffff.md")
	if err := os.WriteFile(broken, []byte("---\nkind: [unclosed\n---\n"), 0o644); err != nil {
		t.Fatalf("write broken entry: %v", err)
	}

	got, err := s.Entries("proj", EntryFilter{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(got) != 1 || got[0].Body != "good" {
		t.Errorf("got %d entries, want the one good entry", len(got))
	}
}

func TestEntriesForMissingProjectIsEmpty(t *testing.T) {
	s := newStore(t)
	got, err := s.Entries("absent", EntryFilter{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries for a project with no log", len(got))
	}
}

func TestResolveConfidence(t *testing.T) {
	for _, tc := range []struct{ kind, requested, want string }{
		{SourceFile, "", ConfidenceHigh},
		{SourceConfluence, "", ConfidenceMedium},
		{SourceInferred, "", ConfidenceLow},
		{"unknown-kind", "", ConfidenceLow},
		// A caller may lower the derived level…
		{SourceFile, ConfidenceLow, ConfidenceLow},
		// …but never raise it: certainty is earned by provenance, not asserted.
		{SourceInferred, ConfidenceHigh, ConfidenceLow},
		{SourceConfluence, ConfidenceHigh, ConfidenceMedium},
		{SourceFile, "nonsense", ConfidenceHigh},
	} {
		if got := ResolveConfidence(tc.kind, tc.requested); got != tc.want {
			t.Errorf("ResolveConfidence(%q, %q) = %q, want %q", tc.kind, tc.requested, got, tc.want)
		}
	}
}

func TestSanitise(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"PROJ-142", "proj-142"},
		{"acme/infrastructure", "acme-infrastructure"},
		{"  spaced  out ", "spaced--out"},
		{"!!!", "entry"},
		{strings.Repeat("a", 100), strings.Repeat("a", 60)},
	} {
		if got := sanitise(tc.in); got != tc.want {
			t.Errorf("sanitise(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
