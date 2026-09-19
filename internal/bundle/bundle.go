// Package bundle assembles what an agent is handed when it opens a task or a
// project, and renders it as markdown.
//
// A bundle is only what the vault holds, plus arithmetic on dates. agentctx
// makes no network calls, so there is no live tracker state here and no search
// hits: the agent calling in already has those interfaces, and a fact it looked
// up enters through log_finding with provenance rather than being copied in
// wholesale by this package.
//
// The consequence is honest and worth stating plainly: the first bundle in a new
// project is nearly empty. The system pays off from the second task onward.
package bundle

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// Marked is a log entry with the staleness verdict attached.
type Marked struct {
	Entry  *store.Entry
	Stale  bool
	Reason string
}

// TaskBundle is what open_task returns.
type TaskBundle struct {
	Task        *store.TaskNote
	TaskBody    string
	Project     *store.ProjectNote
	ProjectBody string

	// Created reports whether this call brought the task note into existence.
	Created bool

	// ResolvedVia says how the task id was arrived at, so "you told me" and
	// "I guessed from a branch name" are not presented as the same thing.
	ResolvedVia string

	// Unconsolidated are the task's own entries not yet folded into the body.
	Unconsolidated []Marked

	// ProjectFindings and RepoFindings are what survives the task — the reason
	// the system exists.
	ProjectFindings []Marked
	RepoFindings    map[string][]Marked
}

// ProjectBundle is what open_project returns.
type ProjectBundle struct {
	Project     *store.ProjectNote
	ProjectBody string
	Created     bool
	Findings    []Marked
	RepoNotes   map[string][]Marked
	OpenTasks   []string
}

// Assembler builds bundles from a store.
type Assembler struct {
	Store      *store.Store
	MaxAgeDays int
	Now        func() time.Time
}

// New returns an Assembler.
func New(s *store.Store, maxAgeDays int) *Assembler {
	return &Assembler{Store: s, MaxAgeDays: maxAgeDays, Now: time.Now}
}

// Task assembles a task bundle.
func (a *Assembler) Task(project, taskID string) (*TaskBundle, error) {
	note, body, err := a.Store.ReadTask(project, taskID)
	if err != nil {
		return nil, err
	}
	projectNote, projectBody, err := a.Store.ReadProject(project)
	if err != nil {
		return nil, err
	}

	b := &TaskBundle{
		Task: note, TaskBody: body,
		Project: projectNote, ProjectBody: projectBody,
		RepoFindings: map[string][]Marked{},
	}

	// Entries the body has already absorbed are represented by the body, so
	// only what came after it is returned separately. Consolidation is
	// compaction; skipping it must never lose a fact.
	var since time.Time
	if note.ConsolidatedThrough != "" {
		if t, err := store.ParseTime(note.ConsolidatedThrough); err == nil {
			since = t
		}
	}

	own, err := a.Store.Entries(project, store.EntryFilter{Task: taskID, Since: since})
	if err != nil {
		return nil, err
	}
	b.Unconsolidated = a.mark(own)

	projectFindings, err := a.Store.Entries(project, store.EntryFilter{
		Scope: store.ScopeProject, Kinds: []string{store.KindFinding},
	})
	if err != nil {
		return nil, err
	}
	b.ProjectFindings = a.mark(projectFindings)

	for _, repo := range note.Repos {
		repoFindings, err := a.Store.Entries(project, store.EntryFilter{
			Scope: store.ScopeRepo, Repos: []string{repo}, Kinds: []string{store.KindFinding},
		})
		if err != nil {
			return nil, err
		}
		if len(repoFindings) > 0 {
			b.RepoFindings[repo] = a.mark(repoFindings)
		}
	}

	return b, nil
}

// Project assembles a project bundle.
func (a *Assembler) Project(projectID string) (*ProjectBundle, error) {
	note, body, err := a.Store.ReadProject(projectID)
	if err != nil {
		return nil, err
	}

	b := &ProjectBundle{Project: note, ProjectBody: body, RepoNotes: map[string][]Marked{}}

	findings, err := a.Store.Entries(projectID, store.EntryFilter{
		Scope: store.ScopeProject, Kinds: []string{store.KindFinding},
	})
	if err != nil {
		return nil, err
	}
	b.Findings = a.mark(findings)

	for _, repo := range note.Repos {
		repoFindings, err := a.Store.Entries(projectID, store.EntryFilter{
			Scope: store.ScopeRepo, Repos: []string{repo}, Kinds: []string{store.KindFinding},
		})
		if err != nil {
			return nil, err
		}
		if len(repoFindings) > 0 {
			b.RepoNotes[repo] = a.mark(repoFindings)
		}
	}

	// Open tasks: every task note with no closed entry. Derived, never stored —
	// a stored status drifts, and this costs one directory walk.
	closed := map[string]bool{}
	closedEntries, err := a.Store.Entries(projectID, store.EntryFilter{Kinds: []string{store.KindClosed}})
	if err != nil {
		return nil, err
	}
	for _, e := range closedEntries {
		closed[e.Task] = true
	}

	tasks, err := a.Store.ListTasks(projectID)
	if err != nil {
		return nil, err
	}
	for _, id := range tasks {
		if !closed[id] {
			b.OpenTasks = append(b.OpenTasks, id)
		}
	}
	sort.Strings(b.OpenTasks)

	return b, nil
}

// mark applies the staleness rules.
func (a *Assembler) mark(entries []*store.Entry) []Marked {
	out := make([]Marked, 0, len(entries))
	for _, e := range entries {
		m := Marked{Entry: e}

		switch {
		case e.Source != nil && e.Source.Kind == store.SourceInferred:
			// An inference was never evidence. Age is beside the point.
			m.Stale = true
			m.Reason = "inferred — not evidence; verify before relying on it"

		case e.Source != nil && e.Source.Retrieved != "":
			retrieved, err := store.ParseTime(e.Source.Retrieved)
			if err != nil {
				m.Stale = true
				m.Reason = fmt.Sprintf("unparseable retrieval date %q — re-verify", e.Source.Retrieved)
				break
			}
			if age := int(a.now().Sub(retrieved).Hours() / 24); age > a.MaxAgeDays {
				m.Stale = true
				m.Reason = fmt.Sprintf("retrieved %d days ago — re-verify", age)
			}
		}
		out = append(out, m)
	}
	return out
}

func (a *Assembler) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// --- rendering ---

// Render turns a task bundle into the markdown an agent reads.
func (b *TaskBundle) Render() string {
	var out strings.Builder

	if b.Created {
		// Says the *note* is new, not that nothing is known: what follows may
		// still be a screenful of project and repository knowledge, which is
		// the entire point of opening a second task in a project.
		fmt.Fprintf(&out, "# %s (new task note)\n", b.Task.ID)
	} else {
		fmt.Fprintf(&out, "# %s\n", b.Task.ID)
	}
	if b.Task.Title != "" {
		fmt.Fprintf(&out, "\n%s\n", b.Task.Title)
	}

	out.WriteString("\n## Task\n\n")
	fmt.Fprintf(&out, "- project: %s\n", b.Task.Project)
	if b.Task.SourceKey != "" {
		fmt.Fprintf(&out, "- source: %s %s\n", b.Task.Source, b.Task.SourceKey)
	}
	if b.Task.SourceURL != "" {
		fmt.Fprintf(&out, "- url: %s\n", b.Task.SourceURL)
	}
	if len(b.Task.Repos) > 0 {
		fmt.Fprintf(&out, "- repos: %s\n", strings.Join(b.Task.Repos, ", "))
	}
	if b.ResolvedVia != "" {
		fmt.Fprintf(&out, "- resolved via: %s\n", b.ResolvedVia)
	}
	fmt.Fprintf(&out, "- note: %s\n", b.Task.ID+".md")

	if body := strings.TrimSpace(stripEmptySections(b.TaskBody)); body != "" {
		out.WriteString("\n## Consolidated so far\n\n")
		out.WriteString(body)
		out.WriteString("\n")
	}

	renderSection(&out, "Since the last consolidation", b.Unconsolidated, true)
	renderSection(&out, "What we know about this project", b.ProjectFindings, false)

	repos := make([]string, 0, len(b.RepoFindings))
	for repo := range b.RepoFindings {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		renderSection(&out, "What we know about "+repo, b.RepoFindings[repo], false)
	}

	if len(b.Unconsolidated) == 0 && len(b.ProjectFindings) == 0 && len(b.RepoFindings) == 0 {
		out.WriteString("\n## Nothing recorded yet\n\n" +
			"This project has no findings. That is expected on the first task in a project — " +
			"the value builds from the second one onward. Log what you learn as you go.\n")
	}

	out.WriteString("\n")
	out.WriteString(WorkingAgreement)
	return out.String()
}

// Render turns a project bundle into markdown.
func (b *ProjectBundle) Render() string {
	var out strings.Builder

	name := b.Project.Name
	if name == "" {
		name = b.Project.ID
	}
	fmt.Fprintf(&out, "# %s", name)
	if b.Created {
		out.WriteString(" (new)")
	}
	out.WriteString("\n\n")

	fmt.Fprintf(&out, "- id: %s\n", b.Project.ID)
	if len(b.Project.JiraProjects) > 0 {
		fmt.Fprintf(&out, "- tracker keys: %s\n", strings.Join(b.Project.JiraProjects, ", "))
	}
	if len(b.Project.Repos) > 0 {
		fmt.Fprintf(&out, "- repos: %s\n", strings.Join(b.Project.Repos, ", "))
	}
	if len(b.Project.ConfluenceSpaces) > 0 {
		fmt.Fprintf(&out, "- wiki spaces: %s\n", strings.Join(b.Project.ConfluenceSpaces, ", "))
	}

	if body := strings.TrimSpace(stripEmptySections(b.ProjectBody)); body != "" {
		out.WriteString("\n## Consolidated so far\n\n")
		out.WriteString(body)
		out.WriteString("\n")
	}

	renderSection(&out, "What we know about this project", b.Findings, false)

	repos := make([]string, 0, len(b.RepoNotes))
	for repo := range b.RepoNotes {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		renderSection(&out, "What we know about "+repo, b.RepoNotes[repo], false)
	}

	if len(b.OpenTasks) > 0 {
		out.WriteString("\n## Open tasks\n\n")
		for _, id := range b.OpenTasks {
			fmt.Fprintf(&out, "- %s\n", id)
		}
		out.WriteString("\nDerived from the log, not stored: a task is open until a closed entry exists.\n")
	}

	return out.String()
}

func renderSection(out *strings.Builder, heading string, items []Marked, showKind bool) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "\n## %s\n\n", heading)
	for _, m := range items {
		e := m.Entry
		label := ""
		if showKind {
			label = strings.ToUpper(e.Kind[:1]) + e.Kind[1:] + " — "
		}
		fmt.Fprintf(out, "- **%s%s**", label, firstLine(e.Body))
		if rest := remainingLines(e.Body); rest != "" {
			fmt.Fprintf(out, "\n\n%s\n", indent(rest, "  "))
		} else {
			out.WriteString("\n")
		}

		var meta []string
		meta = append(meta, e.TS)
		if e.Source != nil && e.Source.Kind != "" {
			ref := e.Source.Kind
			if e.Source.Ref != "" {
				ref += " " + e.Source.Ref
			}
			meta = append(meta, ref)
		}
		if e.Confidence != "" {
			meta = append(meta, "confidence "+e.Confidence)
		}
		if e.Host != "" {
			meta = append(meta, e.Host)
		}
		fmt.Fprintf(out, "  <sub>%s</sub>\n", strings.Join(meta, " · "))

		if m.Stale {
			fmt.Fprintf(out, "  ⚠ **re-verify**: %s\n", m.Reason)
		}
	}
}

// stripEmptySections removes headings with nothing under them, so a freshly
// created note does not return four empty sections as though they were content.
func stripEmptySections(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(line, "#") {
			hasContent := false
			for j := i + 1; j < len(lines); j++ {
				if strings.HasPrefix(lines[j], "#") {
					break
				}
				if strings.TrimSpace(lines[j]) != "" {
					hasContent = true
					break
				}
			}
			if !hasContent {
				continue
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func remainingLines(s string) string {
	s = strings.TrimSpace(s)
	i := strings.Index(s, "\n")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(s[i+1:])
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}
