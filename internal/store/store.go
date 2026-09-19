package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/vault"
)

var (
	// ErrNoProject means a key resolved to no project note. Never auto-create:
	// projects are few and deliberate, and an invented one splits a corpus in
	// two without anyone noticing.
	ErrNoProject = errors.New("no project matches")

	// ErrProjectExists is returned when creating a project that is already there.
	ErrProjectExists = errors.New("project already exists")

	// ErrNoTask means the task note does not exist yet.
	ErrNoTask = errors.New("task note does not exist")
)

// Store reads and writes one agentctx vault.
type Store struct {
	root string

	// now is overridable in tests.
	now func() time.Time
}

// New returns a Store over the vault at root.
func New(root string) *Store {
	return &Store{root: root, now: time.Now}
}

// Root returns the vault path.
func (s *Store) Root() string { return s.root }

// SetClock overrides the clock. Tests only.
func (s *Store) SetClock(f func() time.Time) { s.now = f }

// --- paths ---

// ProjectDir returns <vault>/<project>.
func (s *Store) ProjectDir(project string) string { return filepath.Join(s.root, project) }

// ProjectPath returns <vault>/<project>/<project>.md.
func (s *Store) ProjectPath(project string) string {
	return filepath.Join(s.ProjectDir(project), project+".md")
}

// TaskPath returns <vault>/<project>/tasks/<id>.md.
func (s *Store) TaskPath(project, id string) string {
	return filepath.Join(s.ProjectDir(project), "tasks", id+".md")
}

// LogDir returns <vault>/<project>/log.
func (s *Store) LogDir(project string) string {
	return filepath.Join(s.ProjectDir(project), "log")
}

// NotesDir returns <vault>/<project>/notes.
func (s *Store) NotesDir(project string) string {
	return filepath.Join(s.ProjectDir(project), "notes")
}

// --- projects ---

// ListProjects returns the ids of every project in the vault.
//
// A directory counts as a project only if it holds its own project note, so
// Obsidian's own folders and any stray directory are skipped rather than
// reported as half-projects.
func (s *Store) ListProjects() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("read vault %s: %w", s.root, err)
	}

	var ids []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if vault.Exists(s.ProjectPath(e.Name())) {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ReadProject reads a project note.
func (s *Store) ReadProject(id string) (*ProjectNote, string, error) {
	var note ProjectNote
	body, err := vault.ReadInto(s.ProjectPath(id), &note)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("%w: %q", ErrNoProject, id)
		}
		return nil, "", err
	}
	return &note, body, nil
}

// CreateProject writes a new project note with an empty body.
func (s *Store) CreateProject(note *ProjectNote) error {
	if note.ID == "" {
		return errors.New("project id is required")
	}
	if note.Created == "" {
		note.Created = s.now().Format(LayoutDate)
	}
	name := note.Name
	if name == "" {
		name = note.ID
	}

	data, err := vault.Render(note, fmt.Sprintf(emptyProjectBody, name))
	if err != nil {
		return err
	}
	if err := vault.Create(s.ProjectPath(note.ID), data); err != nil {
		if errors.Is(err, vault.ErrExists) {
			return fmt.Errorf("%w: %q", ErrProjectExists, note.ID)
		}
		return err
	}

	// Create the folders a project is expected to have, so the layout is
	// obvious in Obsidian before anything has been logged into it.
	for _, dir := range []string{s.LogDir(note.ID), s.NotesDir(note.ID), filepath.Join(s.ProjectDir(note.ID), "tasks")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	slog.Info("created project", "project", note.ID)
	return nil
}

// ResolveProjectForKey maps an issue key such as PROJ-142 to a project id,
// using the jira_projects and gitlab_groups lists in the project notes.
//
// Matching is on the key prefix and is case-insensitive. An unknown prefix is
// ErrNoProject and the caller must refuse: guessing here attaches a task to the
// wrong corpus silently, which is the failure this whole layer exists to avoid.
func (s *Store) ResolveProjectForKey(key string) (string, error) {
	prefix := key
	if i := strings.LastIndex(key, "-"); i > 0 {
		prefix = key[:i]
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return "", fmt.Errorf("%w: empty key", ErrNoProject)
	}

	ids, err := s.ListProjects()
	if err != nil {
		return "", err
	}

	for _, id := range ids {
		note, _, err := s.ReadProject(id)
		if err != nil {
			slog.Warn("skipping unreadable project note", "project", id, "err", err)
			continue
		}
		if strings.EqualFold(id, prefix) {
			return id, nil
		}
		for _, candidate := range append(append([]string{}, note.JiraProjects...), note.GitLabGroups...) {
			if strings.EqualFold(strings.TrimSpace(candidate), prefix) {
				return id, nil
			}
		}
	}
	return "", fmt.Errorf("%w for key %q: run open_project first", ErrNoProject, key)
}

// --- tasks ---

// ReadTask reads a task note.
func (s *Store) ReadTask(project, id string) (*TaskNote, string, error) {
	var note TaskNote
	body, err := vault.ReadInto(s.TaskPath(project, id), &note)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", fmt.Errorf("%w: %q", ErrNoTask, id)
		}
		return nil, "", err
	}
	return &note, body, nil
}

// TaskExists reports whether a task note is already in the vault.
func (s *Store) TaskExists(project, id string) bool {
	return vault.Exists(s.TaskPath(project, id))
}

// CreateTask writes a new task note with empty body sections.
func (s *Store) CreateTask(note *TaskNote) error {
	if note.ID == "" {
		return errors.New("task id is required")
	}
	if note.Project == "" {
		return errors.New("task project is required")
	}
	if note.Created == "" {
		note.Created = s.now().Format(LayoutDate)
	}
	if note.Source == "" {
		note.Source = "manual"
	}
	title := note.Title
	if title == "" {
		title = note.ID
	}

	data, err := vault.Render(note, fmt.Sprintf(emptyTaskBody, title))
	if err != nil {
		return err
	}
	if err := vault.Create(s.TaskPath(note.Project, note.ID), data); err != nil {
		return err
	}

	slog.Info("created task", "task", note.ID, "project", note.Project)
	return nil
}

// FindTask locates a task across all projects. Used when a caller knows the id
// but not the project, which is the common case for a key with no prefix
// mapping yet.
func (s *Store) FindTask(id string) (project string, err error) {
	ids, err := s.ListProjects()
	if err != nil {
		return "", err
	}
	for _, p := range ids {
		if s.TaskExists(p, id) {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrNoTask, id)
}

// --- log ---

// Append writes an entry as a new file and returns its path.
//
// The filename carries a random suffix so two hosts writing in the same second
// cannot collide; if they do anyway, Create reports it and this retries rather
// than overwriting.
func (s *Store) Append(entry *Entry) (string, error) {
	if entry.Project == "" {
		return "", errors.New("entry project is required")
	}
	if entry.Kind == "" {
		return "", errors.New("entry kind is required")
	}
	if entry.TS == "" {
		entry.TS = s.now().Format(LayoutStamp)
	}
	if entry.Scope == "" {
		entry.Scope = ScopeTask
	}
	if entry.Scope == ScopeRepo && entry.Repo == "" {
		return "", errors.New("scope is repo but no repo was given")
	}

	stamp, err := ParseTime(entry.TS)
	if err != nil {
		return "", fmt.Errorf("entry timestamp: %w", err)
	}

	subject := entry.Task
	if subject == "" {
		subject = entry.Project
	}

	data, err := vault.Render(entry, entry.Body)
	if err != nil {
		return "", err
	}

	dir := s.LogDir(entry.Project)
	for attempt := range 8 {
		name := fmt.Sprintf("%s-%s-%s-%s.md",
			stamp.Format(LayoutFilename), sanitise(subject), entry.Kind, randomSuffix())
		path := filepath.Join(dir, name)

		err := vault.Create(path, data)
		if err == nil {
			entry.Path = path
			slog.Info("appended entry", "kind", entry.Kind, "scope", entry.Scope, "path", path)
			return path, nil
		}
		if !errors.Is(err, vault.ErrExists) {
			return "", err
		}
		slog.Warn("log filename collision, retrying", "path", path, "attempt", attempt+1)
	}
	return "", errors.New("could not find a free log filename after 8 attempts")
}

// EntryFilter narrows a log query. Zero values mean "no constraint".
type EntryFilter struct {
	Task   string
	Scope  string
	Repos  []string
	Kinds  []string
	Since  time.Time
	Latest bool
}

// Entries returns a project's log entries, oldest first, matching the filter.
//
// Every call walks and parses the project's log directory. That is a few
// milliseconds for the volumes one person produces, and it keeps the files the
// single source of truth with no index to fall out of step.
func (s *Store) Entries(project string, filter EntryFilter) ([]*Entry, error) {
	dir := s.LogDir(project)
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read log %s: %w", dir, err)
	}

	var out []*Entry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") || strings.HasPrefix(de.Name(), ".") {
			continue
		}
		path := filepath.Join(dir, de.Name())

		var entry Entry
		body, err := vault.ReadInto(path, &entry)
		if err != nil {
			// One malformed file must not take out the bundle it appears in.
			slog.Warn("skipping unreadable log entry", "path", path, "err", err)
			continue
		}
		entry.Body = strings.TrimSpace(body)
		entry.Path = path

		if matches(&entry, filter) {
			out = append(out, &entry)
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Time().Before(out[j].Time()) })
	return out, nil
}

func matches(e *Entry, f EntryFilter) bool {
	if f.Task != "" && e.Task != f.Task {
		return false
	}
	if f.Scope != "" && e.Scope != f.Scope {
		return false
	}
	if len(f.Repos) > 0 {
		found := false
		for _, r := range f.Repos {
			if strings.EqualFold(r, e.Repo) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.Kinds) > 0 {
		found := false
		for _, k := range f.Kinds {
			if k == e.Kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if !f.Since.IsZero() {
		ts, err := ParseTime(e.TS)
		if err != nil || !ts.After(f.Since) {
			return false
		}
	}
	return true
}

// sanitise makes a string safe for a filename without making it unreadable.
func sanitise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ', r == '/', r == '.', r == ':':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "entry"
	}
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func randomSuffix() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Only reachable if the system entropy source fails; the timestamp and
		// the collision retry still keep filenames unique.
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

// ListTasks returns the ids of every task note in a project.
func (s *Store) ListTasks(project string) ([]string, error) {
	dir := filepath.Join(s.ProjectDir(project), "tasks")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read tasks %s: %w", dir, err)
	}

	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".md") || strings.HasPrefix(name, ".") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".md"))
	}
	sort.Strings(ids)
	return ids, nil
}
