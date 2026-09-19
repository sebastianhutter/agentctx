package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/sebastianhutter/agentctx/internal/store"
)

func TestConsolidateTask(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogDecision(LogDecisionRequest{
		TaskID: "proj-142", Decision: "chose B", Rationale: "cheaper",
	}); err != nil {
		t.Fatalf("LogDecision: %v", err)
	}

	path, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj-142",
		Body:     "# Rework\n\n## Decisions\n\n- Chose B because it is cheaper.\n",
	})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if !strings.HasSuffix(path, "proj-142.md") {
		t.Errorf("path = %q", path)
	}

	note, body, err := s.Store.ReadTask("proj", "proj-142")
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	if !strings.Contains(body, "Chose B because it is cheaper") {
		t.Errorf("body was not written:\n%s", body)
	}
	if note.ConsolidatedThrough == "" {
		t.Error("consolidated_through was not set")
	}

	// A fold must cover its own record, or every consolidation leaves one
	// entry dangling for ever.
	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"})
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	for _, m := range b.Unconsolidated {
		if strings.Contains(m.Entry.Body, "Consolidated") {
			t.Error("the consolidation record is itself unconsolidated")
		}
	}
}

// Consolidation is compaction, never correctness: work logged after a fold has
// to keep coming back.
func TestConsolidateDoesNotHideLaterEntries(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj-142", Body: "# Rework\n\n## Decisions\n\nNothing yet.\n",
	}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if _, err := s.LogFinding(LogFindingRequest{
		TargetID: "proj-142", Finding: "learned afterwards",
		Source: store.Source{Kind: store.SourceConversation},
	}); err != nil {
		t.Fatalf("LogFinding: %v", err)
	}

	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-142"})
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	found := false
	for _, m := range b.Unconsolidated {
		if strings.Contains(m.Entry.Body, "learned afterwards") {
			found = true
		}
	}
	if !found {
		t.Error("a finding logged after the fold was swallowed by it")
	}
}

// The one place two writers could lose each other's work.
func TestConsolidateRefusesAStaleExpectation(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj-142", Body: "first\n", ExpectThrough: "", Checked: true,
	}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	_, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj-142", Body: "second, composed from a stale read\n",
		ExpectThrough: "", Checked: true,
	})
	if !errors.Is(err, store.ErrConsolidationConflict) {
		t.Fatalf("Consolidate returned %v, want a conflict", err)
	}

	_, body, err := s.Store.ReadTask("proj", "proj-142")
	if err != nil {
		t.Fatalf("ReadTask: %v", err)
	}
	if !strings.Contains(body, "first") {
		t.Error("the refused fold overwrote the note anyway")
	}
}

func TestConsolidateRepoNote(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	if _, err := s.LogFinding(LogFindingRequest{
		TargetID: "proj-142", Finding: "the module breaks on upgrade",
		Scope: store.ScopeRepo, Repo: "acme/infrastructure",
		Source: store.Source{Kind: store.SourceFile, Ref: "modules/vpc/main.tf@abc1234"},
	}); err != nil {
		t.Fatalf("LogFinding: %v", err)
	}

	path, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj", Repo: "acme/infrastructure",
		Body: "# acme/infrastructure\n\n- The module breaks on upgrade.\n",
	})
	if err != nil {
		t.Fatalf("Consolidate repo: %v", err)
	}
	if !strings.Contains(path, "notes/acme-infrastructure.md") {
		t.Errorf("path = %q, want it under notes/", path)
	}

	note, body, err := s.Store.ReadRepoNote("proj", "acme/infrastructure")
	if err != nil {
		t.Fatalf("ReadRepoNote: %v", err)
	}
	if note.Repo != "acme/infrastructure" || note.Project != "proj" {
		t.Errorf("note = %+v", note)
	}
	if !strings.Contains(body, "breaks on upgrade") {
		t.Errorf("body = %q", body)
	}
}

func TestConsolidateRejections(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	mustOpenTask(t, s)

	// Consolidating to nothing would delete the note's contents.
	if _, err := s.Consolidate(ConsolidateRequest{TargetID: "proj-142", Body: "  \n\n"}); err == nil {
		t.Error("an empty body was accepted")
	}
	if _, err := s.Consolidate(ConsolidateRequest{TargetID: "nope", Body: "x"}); err == nil {
		t.Error("an unknown target was accepted")
	}
	// A repository's findings belong to a project, not to one task.
	if _, err := s.Consolidate(ConsolidateRequest{
		TargetID: "proj-142", Repo: "acme/infrastructure", Body: "x",
	}); err == nil {
		t.Error("a repo fold against a task was accepted")
	}
}
