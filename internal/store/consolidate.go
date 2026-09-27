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

// SubjectNote is the frontmatter of a page about an environment or a service,
// notes/environments/<id>.md or notes/services/<id>.md, folded out of the
// findings scoped to it. Repositories keep RepoNote and their place directly
// under notes/, where they were first.
type SubjectNote struct {
	ID                  string `yaml:"id"`
	Kind                string `yaml:"kind"`
	Environment         string `yaml:"environment,omitempty"`
	Service             string `yaml:"service,omitempty"`
	Project             string `yaml:"project"`
	Created             string `yaml:"created"`
	ConsolidatedThrough string `yaml:"consolidated_through,omitempty"`

	// Parent links the note to its project note. See PropParent.
	Parent string `yaml:"parent,omitempty"`
}

// Subject returns what the note is about.
func (n SubjectNote) Subject() Subject {
	id := n.Environment
	if n.Kind == ScopeService {
		id = n.Service
	}
	return Subject{Kind: n.Kind, ID: id}
}

// subjectDirs are the folders under notes/ that hold each kind's pages. A
// folder per kind, so acme/production the environment and acme/production the
// repository cannot land on one file.
var subjectDirs = map[string]string{
	ScopeEnvironment: "environments",
	ScopeService:     "services",
}

// SubjectNotePath returns where a subject's page lives: notes/<repo>.md for a
// repository, notes/environments/<id>.md and notes/services/<id>.md for the
// others.
func (s *Store) SubjectNotePath(project string, sub Subject) string {
	if dir, ok := subjectDirs[sub.Kind]; ok {
		return filepath.Join(s.NotesDir(project), dir, sanitise(sub.ID)+".md")
	}
	return s.RepoNotePath(project, sub.ID)
}

// SubjectNoteExists reports whether a subject's page has been written.
func (s *Store) SubjectNoteExists(project string, sub Subject) bool {
	return vault.Exists(s.SubjectNotePath(project, sub))
}

// ReadSubjectNote reads the page about an environment or a service.
func (s *Store) ReadSubjectNote(project string, sub Subject) (*SubjectNote, string, error) {
	if err := ValidateID("project", project); err != nil {
		return nil, "", err
	}
	if _, ok := subjectDirs[sub.Kind]; !ok {
		return nil, "", fmt.Errorf("%q has no subject page of its own: read a repository with ReadRepoNote", sub.Kind)
	}
	if err := ValidateSubject(sub.Kind, sub.ID); err != nil {
		return nil, "", err
	}
	var note SubjectNote
	body, err := vault.ReadInto(s.SubjectNotePath(project, sub), &note)
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
	note, _, err := s.readProjectNote(projectID)
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

// ConsolidateSubject replaces the body of a subject's page, creating it if
// missing. A repository goes to ConsolidateRepo, so its page stays where it
// always was.
func (s *Store) ConsolidateSubject(projectID string, sub Subject, body, through, expectThrough string, checked bool) error {
	if sub.Kind == ScopeRepo {
		return s.ConsolidateRepo(projectID, sub.ID, body, through, expectThrough, checked)
	}
	note, _, err := s.ReadSubjectNote(projectID, sub)
	switch {
	case err == nil:
		if checked && note.ConsolidatedThrough != expectThrough {
			return fmt.Errorf("%w: it is now at %q, you were working from %q — re-read it and fold again",
				ErrConsolidationConflict, note.ConsolidatedThrough, expectThrough)
		}
	case os.IsNotExist(err):
		note = &SubjectNote{
			ID:      sanitise(sub.ID),
			Kind:    sub.Kind,
			Project: projectID,
			Created: s.now().Format(LayoutDate),
		}
		if sub.Kind == ScopeEnvironment {
			note.Environment = sub.ID
		} else {
			note.Service = sub.ID
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
	if err := rewrite(s.SubjectNotePath(projectID, sub), data); err != nil {
		return err
	}
	slog.Info("consolidated subject note", "project", projectID, "subject", sub.String(), "through", through)
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
