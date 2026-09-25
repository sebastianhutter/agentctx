package store

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastianhutter/agentctx/internal/vault"
	"gopkg.in/yaml.v3"
)

// Link properties. Written into frontmatter as `[[path|display]]` so Obsidian
// shows them as links and anything indexing the vault can follow them.
//
// The ids they are built from (`task`, `project`, `repo`) stay as they are and
// remain what agentctx reads. A link is a rendering of an id, not a second
// source of truth: it is derived from values that never change once written,
// so unlike `status` it cannot go stale.
const (
	// PropParent points one level up: an entry at its task, or at its project
	// when it has no task; a task or repo note at its project.
	//
	// One parent per file rather than a link to every ancestor, so a project
	// note is linked by its tasks and project-scoped entries, not by every
	// entry of every task.
	PropParent = "parent"

	// PropRepoNote points a repo-scoped entry at the note its findings are
	// folded into.
	PropRepoNote = "repo_note"
)

// noteLink renders a link to a file in the vault.
//
// The target is the vault-relative path, not the bare file name. Task ids are
// often tracker keys, and a vault indexed beside a tracker export holds two
// files called `proj-142.md`; a bare `[[proj-142]]` resolves to whichever a
// resolver prefers. The path cannot be mistaken. The display half keeps it
// reading as the id in Obsidian.
func (s *Store) noteLink(path, display string) string {
	rel, err := filepath.Rel(s.root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(strings.TrimSuffix(rel, filepath.Ext(rel)))
	return "[[" + rel + "|" + display + "]]"
}

// ProjectLink returns a link to a project note.
func (s *Store) ProjectLink(project string) string {
	return s.noteLink(s.ProjectPath(project), project)
}

// TaskLink returns a link to a task note.
func (s *Store) TaskLink(project, id string) string {
	return s.noteLink(s.TaskPath(project, id), id)
}

// TaskLinks renders several task ids as links, comma separated.
func (s *Store) TaskLinks(project string, ids []string) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.TaskLink(project, id))
	}
	return strings.Join(out, ", ")
}

// RepoNoteLink returns a link to a repository note.
func (s *Store) RepoNoteLink(project, repo string) string {
	return s.noteLink(s.RepoNotePath(project, repo), repo)
}

// entryLinks works out the link properties an entry should carry.
//
// Only files that exist are linked. A dangling link in Obsidian is one click
// from creating an empty file at that path, and an empty file where a repo note
// belongs is one consolidation cannot read. A repo note that appears later is
// picked up by Relink.
func (s *Store) entryLinks(e *Entry) (parent, repoNote string) {
	if e.Task != "" && vault.Exists(s.TaskPath(e.Project, e.Task)) {
		parent = s.TaskLink(e.Project, e.Task)
	} else if vault.Exists(s.ProjectPath(e.Project)) {
		parent = s.ProjectLink(e.Project)
	}
	if e.Repo != "" && vault.Exists(s.RepoNotePath(e.Project, e.Repo)) {
		repoNote = s.RepoNoteLink(e.Project, e.Repo)
	}
	return parent, repoNote
}

// RelinkReport says what Relink changed, or would change.
type RelinkReport struct {
	Scanned int
	Changed []string // vault-relative paths
}

// Relink adds link properties to files written before agentctx wrote them, and
// qualifies the bare `[[id]]` links in older dependency entries.
//
// The one deliberate exception to "log entries are never edited", and a
// narrow one: it adds link properties derived from ids already in the file and
// rewrites a dependency entry's links to the same ids. No fact changes. The
// frontmatter is edited as text rather than re-rendered, so fields a human
// added in Obsidian and the existing key order survive.
//
// Idempotent: a second run changes nothing, except to add a repo_note link to
// an entry whose repo note has appeared since.
func (s *Store) Relink(dryRun bool) (*RelinkReport, error) {
	projects, err := s.ListProjects()
	if err != nil {
		return nil, err
	}
	report := &RelinkReport{}

	for _, project := range projects {
		projectLink := s.ProjectLink(project)

		taskIDs, err := s.ListTasks(project)
		if err != nil {
			return nil, err
		}
		for _, id := range taskIDs {
			if err := s.relinkFile(report, s.TaskPath(project, id), dryRun, func(doc []byte) ([]byte, error) {
				return setProperties(doc, []property{{PropParent, projectLink}})
			}); err != nil {
				return nil, err
			}
		}

		repoNotes, err := filepath.Glob(filepath.Join(s.NotesDir(project), "*.md"))
		if err != nil {
			return nil, err
		}
		for _, path := range repoNotes {
			var note RepoNote
			if _, err := vault.ReadInto(path, &note); err != nil || note.Repo == "" {
				continue // not a repo note agentctx wrote; leave it alone
			}
			if err := s.relinkFile(report, path, dryRun, func(doc []byte) ([]byte, error) {
				return setProperties(doc, []property{{PropParent, projectLink}})
			}); err != nil {
				return nil, err
			}
		}

		// Replaced entries are still files in the vault, and still deserve links.
		entries, err := s.Entries(project, EntryFilter{IncludeSuperseded: true})
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			parent, repoNote := s.entryLinks(e)
			var props []property
			if parent != "" {
				props = append(props, property{PropParent, parent})
			}
			if repoNote != "" {
				props = append(props, property{PropRepoNote, repoNote})
			}
			if err := s.relinkFile(report, e.Path, dryRun, func(doc []byte) ([]byte, error) {
				out, err := setProperties(doc, props)
				if err != nil {
					return nil, err
				}
				if e.Kind == KindDependency {
					out = s.qualifyTaskLinks(out, project, e.DependsOn)
				}
				return out, nil
			}); err != nil {
				return nil, err
			}
		}
	}
	return report, nil
}

// relinkFile applies edit to one file and writes it back if anything changed.
func (s *Store) relinkFile(report *RelinkReport, path string, dryRun bool, edit func([]byte) ([]byte, error)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	report.Scanned++

	out, err := edit(data)
	if err != nil {
		return fmt.Errorf("relink %s: %w", path, err)
	}
	if bytes.Equal(out, data) {
		return nil
	}

	rel, relErr := filepath.Rel(s.root, path)
	if relErr != nil {
		rel = path
	}
	report.Changed = append(report.Changed, filepath.ToSlash(rel))
	if dryRun {
		return nil
	}
	return rewrite(path, out)
}

// qualifyTaskLinks turns `[[id]]` into a path-qualified link for each id.
// Already-qualified links do not match, which is what makes this idempotent.
func (s *Store) qualifyTaskLinks(data []byte, project string, ids []string) []byte {
	for _, id := range ids {
		data = bytes.ReplaceAll(data, []byte("[["+id+"]]"), []byte(s.TaskLink(project, id)))
	}
	return data
}

type property struct {
	key   string
	value string
}

// setProperties sets top-level frontmatter properties on a markdown file,
// replacing a property's line when it is already there and appending it before
// the closing fence when it is not. Everything else is left byte for byte.
//
// Only single-line values are handled, which is all a link is.
func setProperties(data []byte, props []property) ([]byte, error) {
	if len(props) == 0 {
		return data, nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return nil, fmt.Errorf("no frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	end += 4 // index of the newline before the closing fence

	lines := strings.Split(text[4:end], "\n")
	for _, p := range props {
		line, err := renderProperty(p)
		if err != nil {
			return nil, err
		}
		replaced := false
		for i, l := range lines {
			if strings.HasPrefix(l, p.key+":") {
				lines[i] = line
				replaced = true
				break
			}
		}
		if !replaced {
			lines = append(lines, line)
		}
	}
	return []byte("---\n" + strings.Join(lines, "\n") + text[end:]), nil
}

// renderProperty renders one property the way the yaml encoder would, so a
// relinked file quotes its links exactly as a freshly written one does.
func renderProperty(p property) (string, error) {
	out, err := yaml.Marshal(map[string]string{p.key: p.value})
	if err != nil {
		return "", fmt.Errorf("marshal %s: %w", p.key, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
