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
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// consolidationNudge is how many unfolded entries it takes before a bundle
// mentions consolidating.
//
// Five, because the first real project produced seventy-two entries in three
// minutes and left every note body empty: the failure is not that folding is
// hard, it is that nothing ever asks for it.
const consolidationNudge = 5

// Marked is a log entry with the staleness verdict attached.
type Marked struct {
	Entry  *store.Entry
	Stale  bool
	Reason string

	// From is the project the entry was read from, set only when that is not
	// the bundle's own. Taken from the folder, not the entry's frontmatter,
	// because the folder is where supersedes has to be sent.
	From string
}

// TaskBundle is what open_task returns.
type TaskBundle struct {
	Task        *store.TaskNote
	TaskBody    string
	Project     *store.ProjectNote
	ProjectBody string

	// Created reports whether this call brought the task note into existence.
	Created bool

	// OpenBlockers are tasks this one is waiting for that are not closed yet.
	// First thing rendered when non-empty: starting blocked work is the
	// expensive mistake this graph exists to prevent.
	OpenBlockers []string

	// ClosedBlockers were blockers once and are done, kept so the ordering that
	// was recorded is still visible.
	ClosedBlockers []string

	// Blocks are the tasks waiting for this one.
	Blocks []string

	// Gaps are things this task shows its project is missing, each with the
	// call that closes it. See service.ProjectGaps.
	Gaps []Gap

	// Unconsolidated are the task's own entries not yet folded into the body.
	Unconsolidated []Marked

	// ProjectFindings and RepoFindings are what survives the task — the reason
	// the system exists.
	ProjectFindings []Marked
	RepoFindings    map[string][]Marked

	// SubjectFindings are the project's findings about its environments and
	// services. Every task in the project gets them, as it gets project-scoped
	// findings: a task names its repositories, but nothing names its
	// environments.
	SubjectFindings map[store.Subject][]Marked

	// ElsewhereFindings are findings about the same subjects — the task's
	// repositories, the project's environments and services — that other
	// projects logged. A subject id names one thing wherever it appears, so
	// what another workstream learned there is as true here. Kept apart, and
	// as headlines only, because many are pinned to a moment, or say "this
	// project", that belonged to the project that wrote them.
	ElsewhereFindings map[store.Subject][]Marked
}

// ProjectBundle is what open_project returns.
type ProjectBundle struct {
	Project     *store.ProjectNote
	ProjectBody string
	Created     bool
	Findings    []Marked
	RepoNotes   map[string][]Marked

	// SubjectNotes are the findings about the project's environments and
	// services.
	SubjectNotes map[store.Subject][]Marked

	// ElsewhereNotes are the project's subjects as other projects know them.
	// See TaskBundle.ElsewhereFindings.
	ElsewhereNotes map[store.Subject][]Marked

	// OpenTasks are the ones that can be started now.
	OpenTasks []string

	// BlockedTasks are open but waiting. Separated rather than filtered out:
	// "nothing to do" and "nothing you can do yet" call for different responses.
	BlockedTasks []BlockedTask
}

// Gap is something missing from a project, with the call that closes it.
type Gap struct {
	Message string
	Fix     string
}

// BlockedTask is an open task waiting for others.
type BlockedTask struct {
	ID      string
	Waiting []string
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

	graph, err := a.Store.DependencyGraph(project)
	if err != nil {
		return nil, err
	}
	for _, dep := range graph.BlockedBy[taskID] {
		if graph.Closed[dep] {
			b.ClosedBlockers = append(b.ClosedBlockers, dep)
		} else {
			b.OpenBlockers = append(b.OpenBlockers, dep)
		}
	}
	b.Blocks = graph.Blocks[taskID]

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

	if b.SubjectFindings, b.ElsewhereFindings, err = a.subjects(project, note.Repos); err != nil {
		return nil, err
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

	if b.SubjectNotes, b.ElsewhereNotes, err = a.subjects(projectID, note.Repos); err != nil {
		return nil, err
	}

	// Open tasks: every task note with no closed entry. Derived, never stored —
	// a stored status drifts, and this costs one directory walk.
	graph, err := a.Store.DependencyGraph(projectID)
	if err != nil {
		return nil, err
	}

	tasks, err := a.Store.ListTasks(projectID)
	if err != nil {
		return nil, err
	}
	for _, id := range tasks {
		if graph.Closed[id] {
			continue
		}
		if blockers := graph.OpenBlockers(id); len(blockers) > 0 {
			b.BlockedTasks = append(b.BlockedTasks, BlockedTask{ID: id, Waiting: blockers})
			continue
		}
		b.OpenTasks = append(b.OpenTasks, id)
	}
	sort.Strings(b.OpenTasks)
	sort.Slice(b.BlockedTasks, func(i, j int) bool { return b.BlockedTasks[i].ID < b.BlockedTasks[j].ID })

	return b, nil
}

// subjects collects what is known about a project's environments and services,
// and what other projects know about those and about repos.
//
// repos are the repositories the bundle already shows the project's own
// findings for: a task's, or the project's. Environments and services have no
// such per-task list, so they are the project's (see store.ProjectSubjects).
func (a *Assembler) subjects(project string, repos []string) (own, elsewhere map[store.Subject][]Marked, err error) {
	own = map[store.Subject][]Marked{}
	elsewhere = map[store.Subject][]Marked{}
	for _, kind := range store.SubjectKinds() {
		ids := repos
		if kind != store.ScopeRepo {
			if ids, err = a.Store.ProjectSubjects(project, kind); err != nil {
				return nil, nil, err
			}
			for _, id := range ids {
				entries, err := a.Store.Entries(project, store.EntryFilter{
					Scope: kind, Subjects: []string{id}, Kinds: []string{store.KindFinding},
				})
				if err != nil {
					return nil, nil, err
				}
				if len(entries) > 0 {
					own[store.Subject{Kind: kind, ID: id}] = a.mark(entries)
				}
			}
		}
		if err := a.elsewhere(elsewhere, project, kind, ids); err != nil {
			return nil, nil, err
		}
	}
	return own, elsewhere, nil
}

// elsewhere adds the findings about subjects of one kind that every project
// other than this one has logged, keyed by the subject as this project spells
// it.
//
// Only reads move. Each entry stays in the project that wrote it, so nothing
// here weakens append-only; it is the same fold-on-read as amendments.
func (a *Assembler) elsewhere(out map[store.Subject][]Marked, project, kind string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	projects, err := a.Store.ListProjects()
	if err != nil {
		return err
	}
	for _, other := range projects {
		if other == project {
			continue
		}
		entries, err := a.Store.Entries(other, store.EntryFilter{
			Scope: kind, Subjects: ids, Kinds: []string{store.KindFinding},
		})
		if err != nil {
			// Another project's broken folder must not take out this bundle.
			slog.Warn("skipping unreadable project log", "project", other, "err", err)
			continue
		}
		for i, m := range a.mark(entries) {
			m.From = other
			sub, _ := entries[i].Subject()
			for _, id := range ids {
				if strings.EqualFold(id, sub.ID) {
					key := store.Subject{Kind: kind, ID: id}
					out[key] = append(out[key], m)
					break
				}
			}
		}
	}
	return nil
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

	// Before anything else that is known about the task: if it cannot be
	// started, that is what the next tool call should be about.
	if len(b.OpenBlockers) > 0 {
		fmt.Fprintf(&out, "\n## ⚠ Blocked\n\nWaiting for %s.\n",
			strings.Join(b.OpenBlockers, ", "))
		out.WriteString("Those are not closed yet. Open one to see where it stands, " +
			"or log_dependency with resolved to lift the edge if it no longer applies.\n")
	}

	// Asked for here, where the agent that knows whether the repo or key
	// belongs is reading, rather than on a page nobody opens.
	if len(b.Gaps) > 0 {
		out.WriteString("\n## Project gaps\n\n")
		for _, g := range b.Gaps {
			fmt.Fprintf(&out, "- %s\n  → %s\n", g.Message, g.Fix)
		}
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
	if len(b.ClosedBlockers) > 0 {
		fmt.Fprintf(&out, "- waited for: %s (closed)\n", strings.Join(b.ClosedBlockers, ", "))
	}
	if len(b.Blocks) > 0 {
		fmt.Fprintf(&out, "- blocks: %s\n", strings.Join(b.Blocks, ", "))
	}
	fmt.Fprintf(&out, "- note: %s\n", b.Task.ID+".md")

	if body := strings.TrimSpace(stripEmptySections(b.TaskBody)); body != "" {
		out.WriteString("\n## Consolidated so far\n\n")
		out.WriteString(body)
		out.WriteString("\n")
	}

	renderSection(&out, "Since the last consolidation", b.Unconsolidated, withKind)

	// A note whose body is empty while the log holds everything is a note
	// nobody can read. Say so where it will be noticed, with the number, since
	// the number is what makes it feel worth doing.
	if n := len(b.Unconsolidated); n >= consolidationNudge {
		fmt.Fprintf(&out, "\n> %d entries have not been folded into this note's body yet, so it reads "+
			"as empty in the vault. If you have just finished a stretch of work, consolidate it: "+
			"compose the body sections from the entries above and call consolidate with "+
			"target_id `%s` (consolidated_through `%s`).\n",
			n, b.Task.ID, b.Task.ConsolidatedThrough)
	}
	renderSection(&out, "What we know about this project", b.ProjectFindings, full)

	repos := make([]string, 0, len(b.RepoFindings))
	for repo := range b.RepoFindings {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		renderSection(&out, "What we know about "+repo, b.RepoFindings[repo], full)
	}
	renderSubjects(&out, b.SubjectFindings)
	renderElsewhere(&out, b.ElsewhereFindings)

	if len(b.Unconsolidated) == 0 && len(b.ProjectFindings) == 0 && len(b.RepoFindings) == 0 &&
		len(b.SubjectFindings) == 0 && len(b.ElsewhereFindings) == 0 {
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
	if len(b.Project.Environments) > 0 {
		fmt.Fprintf(&out, "- environments: %s\n", strings.Join(b.Project.Environments, ", "))
	}
	if len(b.Project.Services) > 0 {
		fmt.Fprintf(&out, "- services: %s\n", strings.Join(b.Project.Services, ", "))
	}
	if len(b.Project.ConfluenceSpaces) > 0 {
		fmt.Fprintf(&out, "- wiki spaces: %s\n", strings.Join(b.Project.ConfluenceSpaces, ", "))
	}

	if body := strings.TrimSpace(stripEmptySections(b.ProjectBody)); body != "" {
		out.WriteString("\n## Consolidated so far\n\n")
		out.WriteString(body)
		out.WriteString("\n")
	}

	renderSection(&out, "What we know about this project", b.Findings, full)

	if n := len(b.Findings); n >= consolidationNudge && strings.TrimSpace(stripEmptySections(b.ProjectBody)) == "" {
		fmt.Fprintf(&out, "\n> %d project findings and nothing in the note body: this project reads "+
			"as empty in the vault. Consolidate it with target_id `%s`, and each repository, "+
			"environment and service separately with repo, environment or service set.\n", n, b.Project.ID)
	}

	repos := make([]string, 0, len(b.RepoNotes))
	for repo := range b.RepoNotes {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		renderSection(&out, "What we know about "+repo, b.RepoNotes[repo], full)
	}
	renderSubjects(&out, b.SubjectNotes)
	renderElsewhere(&out, b.ElsewhereNotes)

	if len(b.OpenTasks) > 0 {
		out.WriteString("\n## Ready to start\n\n")
		for _, id := range b.OpenTasks {
			fmt.Fprintf(&out, "- %s\n", id)
		}
	}

	if len(b.BlockedTasks) > 0 {
		out.WriteString("\n## Blocked\n\n")
		for _, t := range b.BlockedTasks {
			fmt.Fprintf(&out, "- %s — waiting for %s\n", t.ID, strings.Join(t.Waiting, ", "))
		}
	}

	if len(b.OpenTasks) > 0 || len(b.BlockedTasks) > 0 {
		out.WriteString("\nBoth lists are folded out of the log, not stored: a task is open until a " +
			"closed entry exists, and a blocker stops blocking when it closes.\n")
	}

	return out.String()
}

// sectionStyle says how renderSection shows each entry.
type sectionStyle int

const (
	// full shows an entry's whole body.
	full sectionStyle = iota
	// withKind is full, prefixed by the entry's kind.
	withKind
	// headline shows the start of an entry's first line only.
	headline
)

// headlineRunes is how much of a finding from another project is shown.
//
// Rendered in full, findings from elsewhere took one real bundle from 14 KB to
// 107 KB: several workstreams' multi-paragraph findings about a shared
// repository. A headline says what is known and where, and open_project on the
// project it came from gives the rest to a session that needs it.
const headlineRunes = 160

// subjectLabel is how a subject is named in a heading. A repository is named
// alone, as it always was; the others say what they are.
func subjectLabel(sub store.Subject) string {
	if sub.Kind == store.ScopeRepo {
		return sub.ID
	}
	return sub.String()
}

// sortedSubjects orders subjects by kind, as SubjectKinds lists them, then by
// id.
func sortedSubjects(bySubject map[store.Subject][]Marked) []store.Subject {
	rank := map[string]int{}
	for i, k := range store.SubjectKinds() {
		rank[k] = i
	}
	subs := make([]store.Subject, 0, len(bySubject))
	for sub := range bySubject {
		subs = append(subs, sub)
	}
	sort.Slice(subs, func(i, j int) bool {
		if subs[i].Kind != subs[j].Kind {
			return rank[subs[i].Kind] < rank[subs[j].Kind]
		}
		return subs[i].ID < subs[j].ID
	})
	return subs
}

// renderSubjects renders the project's own findings about its environments and
// services, one section per subject.
func renderSubjects(out *strings.Builder, bySubject map[store.Subject][]Marked) {
	for _, sub := range sortedSubjects(bySubject) {
		renderSection(out, "What we know about "+subjectLabel(sub), bySubject[sub], full)
	}
}

// renderElsewhere renders what other projects know about a bundle's subjects,
// one section per subject, each entry a headline marked with the project it
// came from.
func renderElsewhere(out *strings.Builder, bySubject map[store.Subject][]Marked) {
	subs := sortedSubjects(bySubject)
	for _, sub := range subs {
		renderSection(out, "What other projects know about "+subjectLabel(sub), bySubject[sub], headline)
	}
	if len(subs) > 0 {
		// Said once, after the sections, where the question comes up.
		out.WriteString("\nThose were logged by other projects working with the same repositories, " +
			"environments or services, and are shown as headlines: open_project on the project named " +
			"under one gives its full text. \"This project\" in one of them means the project it came " +
			"from, and a commit or branch it names was current there, then. To replace one that is " +
			"wrong, log_finding with target_id set to that project and supersedes set to its id.\n")
	}
}

func renderSection(out *strings.Builder, heading string, items []Marked, style sectionStyle) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(out, "\n## %s\n\n", heading)
	for _, m := range items {
		e := m.Entry
		label := ""
		if style == withKind {
			label = strings.ToUpper(e.Kind[:1]) + e.Kind[1:] + " — "
		}
		switch rest := remainingLines(e.Body); {
		case style == headline:
			fmt.Fprintf(out, "- **%s**\n", shorten(firstLine(e.Body), headlineRunes))
		case rest != "":
			fmt.Fprintf(out, "- **%s%s**\n\n%s\n", label, firstLine(e.Body), indent(rest, "  "))
		default:
			fmt.Fprintf(out, "- **%s%s**\n", label, firstLine(e.Body))
		}

		var meta []string
		if m.From != "" {
			meta = append(meta, "from project "+m.From)
		}
		if style == headline {
			// Where it came from and its id are what a headline needs: the
			// source ref and host were most of the length, and open_project on
			// the project named here returns them with the full text.
			meta = append(meta, dateOf(e.TS))
			if e.Confidence != "" {
				meta = append(meta, "confidence "+e.Confidence)
			}
			if e.Path != "" {
				meta = append(meta, "id "+e.ID())
			}
			fmt.Fprintf(out, "  <sub>%s</sub>\n", strings.Join(meta, " · "))
			if m.Stale {
				fmt.Fprintf(out, "  ⚠ **re-verify**: %s\n", m.Reason)
			}
			continue
		}
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
		// The id is what supersedes takes, so an agent that spots a wrong
		// entry here can replace it without hunting for the file.
		if e.Path != "" {
			meta = append(meta, "id "+e.ID())
		}
		fmt.Fprintf(out, "  <sub>%s</sub>\n", strings.Join(meta, " · "))

		if m.Stale {
			fmt.Fprintf(out, "  ⚠ **re-verify**: %s\n", m.Reason)
		}
	}
}

// dateOf returns the date part of a timestamp.
func dateOf(ts string) string {
	if d, _, ok := strings.Cut(ts, "T"); ok {
		return d
	}
	return ts
}

// shorten cuts s to at most n runes, at a word boundary where there is one,
// marking the cut.
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + " …"
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
