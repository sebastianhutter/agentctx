// Package vault reads and writes the markdown files that make up an agentctx
// vault: YAML frontmatter, a markdown body, and nothing else.
//
// Two rules shape this package, and both come from where the vault lives. It is
// a folder on a file-sync service, which means last-write-wins with no locking,
// and several agent sessions write to it at once.
//
//  1. Files are created, never rewritten. A caller that wants to change a fact
//     appends a new file saying so.
//  2. Creation is atomic and refuses to clobber. A half-written file must never
//     be visible to the sync daemon, and two writers racing for one name must
//     produce an error rather than a silently merged file.
package vault

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrExists is returned by Create when the target file is already there.
//
// Worth distinguishing: for a log entry it means a filename collision (retry
// with a new suffix), while for a note it means the note already exists (do not
// create, read it instead).
var ErrExists = errors.New("file already exists")

const fence = "---"

// Doc is a parsed markdown file: frontmatter as a generic map, plus the body.
//
// The generic map exists for readers that must not lose fields they do not know
// about — a human may have added `tags:` in Obsidian, and rewriting the file
// later must not drop it. Writers use typed structs instead, so field order in
// the output is the declaration order rather than yaml's alphabetical sort.
type Doc struct {
	Frontmatter map[string]any
	Body        string
}

// Parse splits a markdown file into frontmatter and body.
//
// A file with no frontmatter is not an error: it parses to an empty map and a
// body, which is what a hand-written note in the vault looks like.
func Parse(data []byte) (*Doc, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")

	fmText, body, ok := split(text)
	if !ok {
		return &Doc{Frontmatter: map[string]any{}, Body: text}, nil
	}

	frontmatter := map[string]any{}
	if strings.TrimSpace(fmText) != "" {
		if err := yaml.Unmarshal([]byte(fmText), &frontmatter); err != nil {
			return nil, fmt.Errorf("parse frontmatter: %w", err)
		}
	}
	return &Doc{Frontmatter: frontmatter, Body: body}, nil
}

// ParseInto splits a markdown file and decodes the frontmatter into v, which
// should be a pointer to a struct with yaml tags. It returns the body.
func ParseInto(data []byte, v any) (string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")

	fmText, body, ok := split(text)
	if !ok {
		return text, errors.New("no frontmatter")
	}
	if err := yaml.Unmarshal([]byte(fmText), v); err != nil {
		return body, fmt.Errorf("parse frontmatter: %w", err)
	}
	return body, nil
}

// split separates a leading --- fenced frontmatter block from the body.
func split(text string) (frontmatter, body string, ok bool) {
	if !strings.HasPrefix(text, fence+"\n") {
		return "", "", false
	}
	rest := text[len(fence)+1:]
	end := strings.Index(rest, "\n"+fence)
	if end < 0 {
		return "", "", false
	}
	frontmatter = rest[:end]
	// Blank lines between the closing fence and the body are convention, not
	// content: trim all of them so a body compares equal however it was written.
	body = strings.TrimLeft(rest[end+len(fence)+1:], "\n")
	return frontmatter, body, true
}

// Render marshals frontmatter and body into a markdown file.
//
// Pass a struct (not a map) to control key order: yaml sorts map keys
// alphabetically, which would scatter a note's identity fields among its
// timestamps and make diffs harder to read than they need to be.
func Render(frontmatter any, body string) ([]byte, error) {
	encoded, err := yaml.Marshal(frontmatter)
	if err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	encoded = unquoteDates(encoded)

	var out strings.Builder
	out.WriteString(fence + "\n")
	out.Write(encoded)
	out.WriteString(fence + "\n")
	if body != "" {
		out.WriteString("\n")
		out.WriteString(strings.TrimLeft(body, "\n"))
		if !strings.HasSuffix(body, "\n") {
			out.WriteString("\n")
		}
	}
	return []byte(out.String()), nil
}

// isoDate matches a quoted date or timestamp emitted for a Go string field.
var isoDate = regexp.MustCompile(`^(\s*[A-Za-z0-9_]+:\s*)"(\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}(?::\d{2})?)?)"\s*$`)

// unquoteDates strips the quotes yaml puts around a date held in a string field.
//
// Cosmetic for a parser, load-bearing for Obsidian: a quoted value is a string
// to Bases, so `created: "2026-09-19"` cannot be sorted or filtered as a date in
// a view. Unquoting is the kind of thing that is trivial now and a migration
// across every file in the vault later.
//
// Safe in the direction that matters: reading goes through typed structs with
// string fields, and yaml decodes an unquoted date straight back into one.
func unquoteDates(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if m := isoDate.FindStringSubmatch(line); m != nil {
			lines[i] = m[1] + m[2]
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// Create writes data to path, creating parent directories, and fails with
// ErrExists rather than overwriting.
//
// The write goes to a temporary file in the same directory, is flushed, and is
// then linked into place. Link is atomic and refuses an existing target, so a
// reader or a sync daemon sees either no file or the whole file, and a race
// between two writers is reported instead of resolved.
func Create(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".agentctx-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := os.Link(tmpName, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s: %w", path, ErrExists)
		}
		return fmt.Errorf("link %s into place: %w", path, err)
	}
	return nil
}

// Read reads and parses a markdown file.
func Read(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// ReadInto reads a markdown file and decodes its frontmatter into v.
func ReadInto(path string, v any) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return ParseInto(data, v)
}

// Exists reports whether path is an existing file.
func Exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
