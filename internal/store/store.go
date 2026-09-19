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

// Match is one hit from Search: a place to look, never the content found there.
type Match struct {
	Kind    string // "project" or "task"
	ID      string
	Title   string
	Project string // empty for a project match
	Repos   []string
	Open    bool   // tasks only
	Why     string // what matched
	Snippet string // one line of context when an entry matched
	Entries int    // how much has been recorded against it
}

// SearchFilter narrows a search. All fields are optional; with none set,
// everything is returned, which is the "what is here at all" case.
type SearchFilter struct {
	// Terms must all appear somewhere in a candidate for it to match.
	Terms []string

	// Repo restricts to projects and tasks touching this repository.
	Repo string

	// IncludeClosed keeps tasks that have a closed entry.
	IncludeClosed bool

	// Limit caps the results. Zero means a sensible default.
	Limit int
}

// Search finds projects and tasks worth opening.
//
// Matching is literal and case-insensitive: every term must appear somewhere in
// the candidate's identity — its id, title, repos, tracker keys — or in the text
// of something logged against it. That last part is what gives the search reach
// without making this a retrieval engine: a query that matches a finding
// surfaces the *task that holds it*, and the finding itself comes back with its
// provenance when the caller opens it.
//
// Deliberately not semantic. A vault indexed by a proper retrieval tool is the
// place for "what is this like"; this answers "what is it called", which is the
// question an agent has to answer before it can call anything else.
func (s *Store) Search(filter SearchFilter) ([]Match, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}

	terms := make([]string, 0, len(filter.Terms))
	for _, t := range filter.Terms {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			terms = append(terms, t)
		}
	}
	repo := strings.ToLower(strings.TrimSpace(filter.Repo))

	projects, err := s.ListProjects()
	if err != nil {
		return nil, err
	}

	var matches []Match
	for _, projectID := range projects {
		note, body, err := s.ReadProject(projectID)
		if err != nil {
			slog.Warn("skipping unreadable project note", "project", projectID, "err", err)
			continue
		}

		entries, err := s.Entries(projectID, EntryFilter{})
		if err != nil {
			return nil, err
		}

		// Index the project's entries by task once, rather than re-reading the
		// log for every task in it.
		byTask := map[string][]*Entry{}
		closed := map[string]bool{}
		var projectScoped []*Entry
		for _, e := range entries {
			if e.Task == "" {
				projectScoped = append(projectScoped, e)
				continue
			}
			byTask[e.Task] = append(byTask[e.Task], e)
			if e.Kind == KindClosed {
				closed[e.Task] = true
			}
		}

		if repo == "" || containsFold(note.Repos, repo) {
			haystack := strings.ToLower(strings.Join(append([]string{
				note.ID, note.Name, body,
			}, append(append(append([]string{}, note.Repos...), note.JiraProjects...), note.GitLabGroups...)...), " "))

			why, snippet, ok := matchWhere(terms, haystack, projectScoped, "project")
			if ok {
				matches = append(matches, Match{
					Kind: "project", ID: note.ID, Title: note.Name, Repos: note.Repos,
					Why: why, Snippet: snippet, Entries: len(entries),
				})
			}
		}

		taskIDs, err := s.ListTasks(projectID)
		if err != nil {
			return nil, err
		}
		for _, taskID := range taskIDs {
			task, taskBody, err := s.ReadTask(projectID, taskID)
			if err != nil {
				slog.Warn("skipping unreadable task note", "task", taskID, "err", err)
				continue
			}
			if !filter.IncludeClosed && closed[taskID] {
				continue
			}
			if repo != "" && !containsFold(task.Repos, repo) {
				continue
			}

			haystack := strings.ToLower(strings.Join(append([]string{
				task.ID, task.Title, task.SourceKey, taskBody,
			}, task.Repos...), " "))

			why, snippet, ok := matchWhere(terms, haystack, byTask[taskID], "task")
			if ok {
				matches = append(matches, Match{
					Kind: "task", ID: task.ID, Title: task.Title, Project: task.Project,
					Repos: task.Repos, Open: !closed[taskID],
					Why: why, Snippet: snippet, Entries: len(byTask[taskID]),
				})
			}
		}
	}

	// Projects first, then tasks with the most recorded against them: a task
	// nobody has written anything about is the least useful thing to offer.
	sort.SliceStable(matches, func(i, j int) bool {
		if (matches[i].Kind == "project") != (matches[j].Kind == "project") {
			return matches[i].Kind == "project"
		}
		if matches[i].Entries != matches[j].Entries {
			return matches[i].Entries > matches[j].Entries
		}
		return matches[i].ID < matches[j].ID
	})

	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

// matchWhere reports whether every term appears in the candidate's own text or
// in something logged against it, and says which.
func matchWhere(terms []string, haystack string, entries []*Entry, kind string) (why, snippet string, ok bool) {
	if len(terms) == 0 {
		return "listed", "", true
	}

	missing := terms[:0:0]
	for _, t := range terms {
		if !strings.Contains(haystack, t) {
			missing = append(missing, t)
		}
	}
	if len(missing) == 0 {
		return "matches this " + kind, "", true
	}

	// Terms not in the note itself may be in what was logged against it, which
	// is the case worth having: "have we hit this before" is a question about
	// the findings, not about the titles.
	for _, e := range entries {
		// Opened entries are boilerplate this tool wrote itself — "Task created
		// from PROJ-142 on kepler-claude-code". Searching them would make every
		// task match its own host name, which is a lot of confident noise.
		if e.Kind == KindOpened {
			continue
		}
		body := strings.ToLower(e.Body)
		all := true
		for _, t := range missing {
			if !strings.Contains(body, t) {
				all = false
				break
			}
		}
		if all {
			return describeMatch(e.Kind), firstLineOf(e.Body), true
		}
	}
	return "", "", false
}

// describeMatch says where a hit came from, in words that read as a sentence.
func describeMatch(kind string) string {
	switch kind {
	case KindClosed:
		return "matches the closing summary"
	case KindNote:
		return "matches a note"
	default:
		return "matches a logged " + kind
	}
}

func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 140 {
		s = s[:140] + "…"
	}
	return s
}

func containsFold(list []string, want string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), want) {
			return true
		}
	}
	return false
}
