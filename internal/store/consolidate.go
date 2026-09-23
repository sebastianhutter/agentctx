package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianhutter/agentctx/internal/vault"
)

// ErrConsolidationConflict means the note moved on since the caller read it.
var ErrConsolidationConflict = errors.New("the note was consolidated by someone else in the meantime")

// RepoNote is the frontmatter of notes/<repo>.md — knowledge about one
// repository, folded out of its repo-scoped findings.
type RepoNote struct {
	ID                  string `yaml:"id"`
	Repo                string `yaml:"repo"`
	Project             string `yaml:"project"`
	Created             string `yaml:"created"`
	ConsolidatedThrough string `yaml:"consolidated_through,omitempty"`

	// Parent links the note to its project note. See PropParent.
	Parent string `yaml:"parent,omitempty"`
}

// RepoNotePath returns <vault>/<project>/notes/<repo>.md.
func (s *Store) RepoNotePath(project, repo string) string {
	return filepath.Join(s.NotesDir(project), sanitise(repo)+".md")
}

// ReadRepoNote reads a repository note.
func (s *Store) ReadRepoNote(project, repo string) (*RepoNote, string, error) {
	if err := ValidateID("project", project); err != nil {
		return nil, "", err
	}
	if err := ValidateRepo(repo); err != nil {
		return nil, "", err
	}
	var note RepoNote
	body, err := vault.ReadInto(s.RepoNotePath(project, repo), &note)
	if err != nil {
		return nil, "", err
	}
	return &note, body, nil
}

// rewrite replaces a note's contents. The only function in this package that
// does, and the reason consolidation is a distinct verb rather than something
// any agent can do: every other write here appends a file.
//
// Writes a new file beside the old one and renames over it, so a reader or a
// sync daemon never sees a half-written note.
func rewrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".agentctx-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
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
	return os.Rename(tmpName, path)
}

// ConsolidateTask replaces a task note's body and records how far it has folded
// the log.
//
// expectThrough guards the one place in this system where two writers could
// lose each other's work: if it does not match what is on disk, the note was
// consolidated while this caller was composing, and overwriting would silently
// drop that. Pass what the bundle reported; pass "" to skip the check.
func (s *Store) ConsolidateTask(project, taskID, body, through, expectThrough string, checked bool) error {
	note, _, err := s.ReadTask(project, taskID)
	if err != nil {
		return err
	}
	if checked && note.ConsolidatedThrough != expectThrough {
		return fmt.Errorf("%w: it is now at %q, you were working from %q — re-read it and fold again",
			ErrConsolidationConflict, note.ConsolidatedThrough, expectThrough)
	}

	note.ConsolidatedThrough = through
	// A note created before links were written gains one on its next fold.
	note.Parent = s.ProjectLink(project)
	data, err := vault.Render(note, body)
	if err != nil {
		return err
	}
	if err := rewrite(s.TaskPath(project, taskID), data); err != nil {
		return err
	}
	slog.Info("consolidated task", "task", taskID, "through", through)
	return nil
}

// ConsolidateProject replaces a project note's body.
func (s *Store) ConsolidateProject(projectID, body, through, expectThrough string, checked bool) error {
	note, _, err := s.ReadProject(projectID)
	if err != nil {
		return err
	}
	if checked && note.ConsolidatedThrough != expectThrough {
		return fmt.Errorf("%w: it is now at %q, you were working from %q — re-read it and fold again",
			ErrConsolidationConflict, note.ConsolidatedThrough, expectThrough)
	}

	note.ConsolidatedThrough = through
	data, err := vault.Render(note, body)
	if err != nil {
		return err
	}
	if err := rewrite(s.ProjectPath(projectID), data); err != nil {
		return err
	}
	slog.Info("consolidated project", "project", projectID, "through", through)
	return nil
}

// ConsolidateRepo replaces a repository note's body, creating it if missing.
//
// This is where the design's promised payoff actually lands: repo-scoped
// findings from many tasks folded into one page about that repository, which
// the next task touching it reads.
func (s *Store) ConsolidateRepo(projectID, repo, body, through, expectThrough string, checked bool) error {
	note, _, err := s.ReadRepoNote(projectID, repo)
	switch {
	case err == nil:
		if checked && note.ConsolidatedThrough != expectThrough {
			return fmt.Errorf("%w: it is now at %q, you were working from %q — re-read it and fold again",
				ErrConsolidationConflict, note.ConsolidatedThrough, expectThrough)
		}
	case os.IsNotExist(err):
		note = &RepoNote{
			ID:      sanitise(repo),
			Repo:    repo,
			Project: projectID,
			Created: s.now().Format(LayoutDate),
		}
	default:
		return err
	}

	note.ConsolidatedThrough = through
	note.Parent = s.ProjectLink(projectID)
	data, err := vault.Render(note, body)
	if err != nil {
		return err
	}
	if err := rewrite(s.RepoNotePath(projectID, repo), data); err != nil {
		return err
	}
	slog.Info("consolidated repo note", "project", projectID, "repo", repo, "through", through)
	return nil
}

// NewestEntry returns the timestamp of the most recent entry matching a filter,
// which is how far a consolidation can honestly claim to have folded.
func (s *Store) NewestEntry(project string, filter EntryFilter) (string, error) {
	entries, err := s.Entries(project, filter)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	return entries[len(entries)-1].TS, nil
}

// TrimBody normalises a composed body: no surrounding blank space, one trailing
// newline.
//
// TrimSpace rather than trimming newlines alone, so a body of whitespace is
// recognised as empty. Consolidating to nothing deletes what a note says, and
// "  \n\n" should not be the way that happens.
func TrimBody(body string) string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	return body + "\n"
}
