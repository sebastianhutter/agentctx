package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withTask(t *testing.T, s *Store, project, id string) {
	t.Helper()
	if err := s.CreateTask(&TaskNote{ID: id, Project: project, Title: "Example task"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestNewFilesCarryParentLinks(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)
	withTask(t, s, "proj", "proj-142")

	if got := readFile(t, s.TaskPath("proj", "proj-142")); !strings.Contains(got, "parent: '[[proj/proj|proj]]'") {
		t.Errorf("task note has no parent link:\n%s", got)
	}

	taskEntry := &Entry{Kind: KindFinding, Task: "proj-142", Project: "proj", Body: "x"}
	if _, err := s.Append(taskEntry); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := readFile(t, taskEntry.Path); !strings.Contains(got, "parent: '[[proj/tasks/proj-142|proj-142]]'") {
		t.Errorf("task entry does not link its task:\n%s", got)
	}

	projectEntry := &Entry{Kind: KindFinding, Project: "proj", Scope: ScopeProject, Body: "x"}
	if _, err := s.Append(projectEntry); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := readFile(t, projectEntry.Path); !strings.Contains(got, "parent: '[[proj/proj|proj]]'") {
		t.Errorf("project entry does not link its project:\n%s", got)
	}
}

func TestRepoNoteLinkedOnlyOnceItExists(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)

	before := &Entry{Kind: KindFinding, Project: "proj", Scope: ScopeRepo, Repo: "acme/infrastructure", Body: "x"}
	if _, err := s.Append(before); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := readFile(t, before.Path); strings.Contains(got, PropRepoNote) {
		t.Errorf("linked a repo note that does not exist:\n%s", got)
	}

	if err := s.ConsolidateRepo("proj", "acme/infrastructure", "body\n", before.TS, "", false); err != nil {
		t.Fatalf("ConsolidateRepo: %v", err)
	}
	if got := readFile(t, s.RepoNotePath("proj", "acme/infrastructure")); !strings.Contains(got, "parent: '[[proj/proj|proj]]'") {
		t.Errorf("repo note has no parent link:\n%s", got)
	}

	// Relink picks up the note that appeared after the entry was written.
	if _, err := s.Relink(false); err != nil {
		t.Fatalf("Relink: %v", err)
	}
	want := "repo_note: '[[proj/notes/acme-infrastructure|acme/infrastructure]]'"
	if got := readFile(t, before.Path); !strings.Contains(got, want) {
		t.Errorf("relink did not add the repo note link:\n%s", got)
	}
}

func TestRelinkBackfillsOldFilesWithoutTouchingAnythingElse(t *testing.T) {
	s := newStore(t)
	withProject(t, s, "proj", nil)
	withTask(t, s, "proj", "proj-139")

	// Files as an older agentctx wrote them: no links, a bare dependency link,
	// and a property a human added in Obsidian.
	task := filepath.Join(s.ProjectDir("proj"), "tasks", "proj-142.md")
	oldTask := "---\nid: proj-142\nsource: manual\nproject: proj\ncreated: 2026-09-19\ntags:\n    - mine\n---\n\n# proj-142\n"
	dep := filepath.Join(s.LogDir("proj"), "2026-09-19T1432-proj-142-dependency-0000.md")
	oldDep := "---\nkind: dependency\ntask: proj-142\nproject: proj\nscope: task\nts: 2026-09-19T14:32:07\nhost: h\ndepends_on:\n    - proj-139\n---\n\nWaits for [[proj-139]].\n\nBecause.\n"
	for path, data := range map[string]string{task: oldTask, dep: oldDep} {
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dry, err := s.Relink(true)
	if err != nil {
		t.Fatalf("Relink dry run: %v", err)
	}
	if len(dry.Changed) == 0 || readFile(t, task) != oldTask {
		t.Fatalf("dry run changed %v or wrote to disk", dry.Changed)
	}

	if _, err := s.Relink(false); err != nil {
		t.Fatalf("Relink: %v", err)
	}
	gotTask := readFile(t, task)
	wantTask := strings.Replace(oldTask, "    - mine\n", "    - mine\nparent: '[[proj/proj|proj]]'\n", 1)
	if gotTask != wantTask {
		t.Errorf("task note:\n got %q\nwant %q", gotTask, wantTask)
	}
	gotDep := readFile(t, dep)
	for _, want := range []string{
		"parent: '[[proj/tasks/proj-142|proj-142]]'\n---",
		"Waits for [[proj/tasks/proj-139|proj-139]].",
		"depends_on:\n    - proj-139\n",
	} {
		if !strings.Contains(gotDep, want) {
			t.Errorf("dependency entry is missing %q:\n%s", want, gotDep)
		}
	}

	// The ids agentctx works from are untouched, so the log still reads the same.
	g, err := s.DependencyGraph("proj")
	if err != nil {
		t.Fatalf("DependencyGraph: %v", err)
	}
	if blockers := g.OpenBlockers("proj-142"); len(blockers) != 1 || blockers[0] != "proj-139" {
		t.Errorf("blockers after relink = %v", blockers)
	}

	again, err := s.Relink(false)
	if err != nil {
		t.Fatalf("second Relink: %v", err)
	}
	if len(again.Changed) != 0 {
		t.Errorf("second run changed %v, want nothing", again.Changed)
	}
}
