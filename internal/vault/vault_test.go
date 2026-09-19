package vault

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseSplitsFrontmatterAndBody(t *testing.T) {
	doc, err := Parse([]byte("---\nid: proj-142\nscope: task\n---\n\nthe body\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Frontmatter["id"] != "proj-142" {
		t.Errorf("id = %v", doc.Frontmatter["id"])
	}
	if doc.Body != "the body\n" {
		t.Errorf("body = %q", doc.Body)
	}
}

// A hand-written note in the vault has no frontmatter. Reading one must not be
// an error, or a stray file breaks a whole bundle.
func TestParseFileWithoutFrontmatter(t *testing.T) {
	doc, err := Parse([]byte("# just a note\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Frontmatter) != 0 {
		t.Errorf("frontmatter = %v, want empty", doc.Frontmatter)
	}
	if doc.Body != "# just a note\n" {
		t.Errorf("body = %q", doc.Body)
	}
}

func TestParseUnterminatedFrontmatterIsBody(t *testing.T) {
	doc, err := Parse([]byte("---\nid: proj-142\nnever closed\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(doc.Frontmatter) != 0 {
		t.Errorf("frontmatter = %v, want empty for an unterminated fence", doc.Frontmatter)
	}
}

func TestParseCRLF(t *testing.T) {
	doc, err := Parse([]byte("---\r\nid: proj-142\r\n---\r\n\r\nbody\r\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Frontmatter["id"] != "proj-142" {
		t.Errorf("id = %v", doc.Frontmatter["id"])
	}
}

func TestRenderRoundTrip(t *testing.T) {
	type fm struct {
		ID    string   `yaml:"id"`
		Scope string   `yaml:"scope"`
		Repos []string `yaml:"repos"`
	}
	data, err := Render(fm{ID: "proj-142", Scope: "task", Repos: []string{"acme/infrastructure"}}, "the body")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	// Declaration order, not yaml's alphabetical map order.
	if got := string(data); strings.Index(got, "id:") > strings.Index(got, "scope:") {
		t.Errorf("field order not preserved:\n%s", got)
	}

	var back fm
	body, err := ParseInto(data, &back)
	if err != nil {
		t.Fatalf("ParseInto: %v", err)
	}
	if back.ID != "proj-142" || back.Scope != "task" || len(back.Repos) != 1 {
		t.Errorf("round trip lost data: %+v", back)
	}
	if strings.TrimSpace(body) != "the body" {
		t.Errorf("body = %q", body)
	}
}

func TestCreateRefusesToClobber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "entry.md")

	if err := Create(path, []byte("first")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	err := Create(path, []byte("second"))
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Create returned %v, want ErrExists", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "first" {
		t.Errorf("content = %q, want the original to survive", data)
	}
}

// The concurrency claim the whole design rests on: parallel writers produce
// either distinct files or a reported error, never a merged or partial one.
func TestCreateConcurrentWritersDoNotCorrupt(t *testing.T) {
	dir := t.TempDir()
	const writers = 16

	var wg sync.WaitGroup
	results := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every writer targets the same name on purpose.
			results[i] = Create(filepath.Join(dir, "contested.md"), []byte(strings.Repeat("x", 4096)))
		}()
	}
	wg.Wait()

	won := 0
	for i, err := range results {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrExists):
		default:
			t.Errorf("writer %d: unexpected error %v", i, err)
		}
	}
	if won != 1 {
		t.Errorf("%d writers succeeded, want exactly 1", won)
	}

	data, err := os.ReadFile(filepath.Join(dir, "contested.md"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) != 4096 {
		t.Errorf("file is %d bytes, want a complete 4096", len(data))
	}

	// No temp files left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries, want only the finished file", len(entries))
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("Exists reported true for a directory")
	}
	path := filepath.Join(dir, "note.md")
	if Exists(path) {
		t.Error("Exists reported true for a missing file")
	}
	if err := Create(path, []byte("x")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !Exists(path) {
		t.Error("Exists reported false for an existing file")
	}
}

// Obsidian Bases treats a quoted value as a string, so a quoted date cannot be
// sorted or filtered as a date in a view.
func TestRenderLeavesDatesUnquoted(t *testing.T) {
	type fm struct {
		Created   string `yaml:"created"`
		Stamp     string `yaml:"ts"`
		Title     string `yaml:"title"`
		NotADate  string `yaml:"key"`
		Retrieved string `yaml:"retrieved"`
	}
	data, err := Render(fm{
		Created: "2026-09-19", Stamp: "2026-09-19T14:32:07",
		Title: "A task named 2026-09-19", NotADate: "PROJ-142", Retrieved: "2026-09-19",
	}, "")
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := string(data)

	for _, want := range []string{"created: 2026-09-19\n", "ts: 2026-09-19T14:32:07\n", "retrieved: 2026-09-19\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	// A title that merely contains a date is not a date.
	if !strings.Contains(got, `title: A task named 2026-09-19`) {
		t.Errorf("title was mangled:\n%s", got)
	}

	// And it still round-trips into string fields, which is how everything reads.
	var back fm
	if _, err := ParseInto(data, &back); err != nil {
		t.Fatalf("ParseInto: %v", err)
	}
	if back.Created != "2026-09-19" || back.Stamp != "2026-09-19T14:32:07" {
		t.Errorf("round trip lost the dates: %+v", back)
	}
}
