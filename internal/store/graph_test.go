package store

import (
	"testing"
	"time"
)

func graphFixture(t *testing.T) *Store {
	t.Helper()
	s := New(t.TempDir())
	s.SetClock(func() time.Time { return time.Date(2026, 9, 19, 14, 0, 0, 0, time.Local) })

	if err := s.CreateProject(&ProjectNote{ID: "proj", Name: "Example"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, id := range []string{"proj-1", "proj-2", "proj-3", "proj-4"} {
		if err := s.CreateTask(&TaskNote{ID: id, Project: "proj", Title: id}); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	return s
}

func depend(t *testing.T, s *Store, task string, ts string, resolved bool, deps ...string) {
	t.Helper()
	if _, err := s.Append(&Entry{
		Kind: KindDependency, Task: task, Project: "proj", TS: ts,
		DependsOn: deps, Resolved: resolved, Body: "because",
	}); err != nil {
		t.Fatalf("Append dependency: %v", err)
	}
}

func TestDependencyGraphFoldsTheLog(t *testing.T) {
	s := graphFixture(t)
	depend(t, s, "proj-2", "2026-09-19T14:01:00", false, "proj-1")
	depend(t, s, "proj-3", "2026-09-19T14:02:00", false, "proj-1", "proj-2")

	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if got := g.BlockedBy["proj-3"]; len(got) != 2 {
		t.Errorf("proj-3 blocked by %v, want two", got)
	}
	if got := g.Blocks["proj-1"]; len(got) != 2 {
		t.Errorf("proj-1 blocks %v, want two", got)
	}
	if g.Ready("proj-2") {
		t.Error("proj-2 is waiting for proj-1 but reports ready")
	}
	if !g.Ready("proj-1") {
		t.Error("proj-1 waits for nothing but reports blocked")
	}
}

// The reason the graph is derived rather than stored: finishing a blocker must
// unblock its dependants without anyone editing a field.
func TestClosedBlockerStopsBlocking(t *testing.T) {
	s := graphFixture(t)
	depend(t, s, "proj-2", "2026-09-19T14:01:00", false, "proj-1")

	if _, err := s.Append(&Entry{
		Kind: KindClosed, Task: "proj-1", Project: "proj", TS: "2026-09-19T14:05:00", Body: "done",
	}); err != nil {
		t.Fatalf("Append closed: %v", err)
	}

	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if !g.Ready("proj-2") {
		t.Errorf("proj-2 still blocked by %v after its blocker closed", g.OpenBlockers("proj-2"))
	}
	// The edge is still recorded, so why it waited stays visible.
	if len(g.BlockedBy["proj-2"]) != 1 {
		t.Error("the recorded ordering was lost when the blocker closed")
	}
}

// Append-only means a change of mind is a later entry, not an edit.
func TestLaterEntryWins(t *testing.T) {
	s := graphFixture(t)
	depend(t, s, "proj-2", "2026-09-19T14:01:00", false, "proj-1")
	depend(t, s, "proj-2", "2026-09-19T14:09:00", true, "proj-1")

	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if !g.Ready("proj-2") {
		t.Error("a resolved dependency still blocks")
	}

	// And it can come back.
	depend(t, s, "proj-2", "2026-09-19T14:20:00", false, "proj-1")
	g, err = s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if g.Ready("proj-2") {
		t.Error("a re-declared dependency does not block")
	}
}

func TestWouldCycle(t *testing.T) {
	s := graphFixture(t)
	depend(t, s, "proj-2", "2026-09-19T14:01:00", false, "proj-1")
	depend(t, s, "proj-3", "2026-09-19T14:02:00", false, "proj-2")

	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}

	// proj-1 waiting for proj-3 closes the loop 1 → 3 → 2 → 1.
	cycle := g.WouldCycle("proj-1", "proj-3")
	if cycle == nil {
		t.Fatal("a transitive loop was not detected")
	}
	if got := FormatCycle(cycle); got != "proj-1 → proj-3 → proj-2 → proj-1" {
		t.Errorf("cycle = %q", got)
	}

	if g.WouldCycle("proj-1", "proj-1") == nil {
		t.Error("a self-dependency was not detected")
	}
	if got := g.WouldCycle("proj-4", "proj-1"); got != nil {
		t.Errorf("a safe edge was reported as a cycle: %v", got)
	}
}

func TestDependencyGraphOnAnEmptyProject(t *testing.T) {
	s := graphFixture(t)

	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if !g.Ready("proj-1") || len(g.BlockedBy) != 0 {
		t.Error("a project with no dependencies is not all-ready")
	}
}
