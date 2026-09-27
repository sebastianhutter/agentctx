package web

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// projectSummary is a row on the index.
type projectSummary struct {
	ID      string
	Name    string
	Repos   []string
	Tasks   int
	Ready   int
	Blocked int
	Closed  int
	Entries int
	Last    string
	Stale   bool // nothing in the note body yet
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	ids, err := st.ListProjects()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	var rows []projectSummary
	for _, id := range ids {
		note, body, err := st.ReadProject(id)
		if err != nil {
			continue
		}
		entries, err := st.Entries(id, store.EntryFilter{})
		if err != nil {
			continue
		}
		graph, err := st.DependencyGraph(id)
		if err != nil {
			continue
		}
		tasks, err := st.ListTasks(id)
		if err != nil {
			continue
		}

		row := projectSummary{
			ID: id, Name: note.Name, Repos: note.Repos,
			Tasks: len(tasks), Entries: len(entries),
			Stale: !hasContent(body),
		}
		for _, t := range tasks {
			switch {
			case graph.Closed[t]:
				row.Closed++
			case graph.Ready(t):
				row.Ready++
			default:
				row.Blocked++
			}
		}
		if len(entries) > 0 {
			row.Last = entries[len(entries)-1].TS
		}
		rows = append(rows, row)
	}

	warn := 0
	if issues, err := st.Check(); err == nil {
		for _, i := range issues {
			if i.Severity == store.SeverityWarn {
				warn++
			}
		}
	}

	s.render(w, "index.html", map[string]any{
		"Page":     "projects",
		"Projects": rows,
		"Warn":     warn,
		"Vault":    cfg.ExpandedVaultPath(),
		"Version":  s.version,
	})
}

// taskRow is a task as listed on a project page.
type taskRow struct {
	ID      string
	Title   string
	Closed  bool
	Waiting []string
	Entries int
}

func (s *Server) handleProject(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	id := r.PathValue("project")

	note, body, err := st.ReadProject(id)
	if err != nil {
		s.fail(w, err, http.StatusNotFound)
		return
	}
	entries, err := st.Entries(id, store.EntryFilter{})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	graph, err := st.DependencyGraph(id)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	taskIDs, err := st.ListTasks(id)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	counts := map[string]int{}
	for _, e := range entries {
		if e.Task != "" {
			counts[e.Task]++
		}
	}

	var ready, blocked, closed []taskRow
	for _, t := range taskIDs {
		note, _, err := st.ReadTask(id, t)
		title := t
		if err == nil && note.Title != "" {
			title = note.Title
		}
		row := taskRow{ID: t, Title: title, Entries: counts[t]}
		switch {
		case graph.Closed[t]:
			row.Closed = true
			closed = append(closed, row)
		case graph.Ready(t):
			ready = append(ready, row)
		default:
			row.Waiting = graph.OpenBlockers(t)
			blocked = append(blocked, row)
		}
	}

	s.render(w, "project.html", map[string]any{
		"Page":         "projects",
		"Project":      note,
		"Body":         markdown(body),
		"HasBody":      strings.TrimSpace(body) != "",
		"Ready":        ready,
		"Blocked":      blocked,
		"Closed":       closed,
		"Repos":        projectSubjects(st, note, entries, store.ScopeRepo),
		"Environments": projectSubjects(st, note, entries, store.ScopeEnvironment),
		"Services":     projectSubjects(st, note, entries, store.ScopeService),
		"Recent":       decorate(lastN(entries, 15), cfg.Stale.MaxAgeDays),
		"Entries":      len(entries),
		"Issues":       projectIssues(st, id),
		"Version":      s.version,
	})
}

// subjectRow is a repo, environment or service as listed on a project page.
type subjectRow struct {
	ID           string
	Consolidated bool
	Findings     int
}

// projectSubjects lists every subject of one kind the project claims, whether
// or not anything has been recorded about it. They are part of what the project
// *is*; listing only the ones with findings makes a new project look like it
// has no repositories at all, and hides the ones still waiting to be written
// about.
func projectSubjects(st *store.Store, note *store.ProjectNote, entries []*store.Entry, kind string) []subjectRow {
	findings := map[string]int{}
	for _, e := range entries {
		if sub, ok := e.Subject(); ok && sub.Kind == kind {
			findings[sub.ID]++
		}
	}
	listed := note.SubjectsOf(kind)
	var rows []subjectRow
	for _, id := range listed {
		sub := store.Subject{Kind: kind, ID: id}
		rows = append(rows, subjectRow{ID: id, Consolidated: subjectNoteExists(st, note.ID, sub), Findings: findings[id]})
	}
	// A subject written about but not listed on the project note still
	// deserves a page — the alternative is knowledge with nowhere to appear.
	for id := range findings {
		if !containsString(listed, id) {
			rows = append(rows, subjectRow{ID: id, Findings: findings[id]})
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows
}

func (s *Server) handleTask(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	projectID := r.PathValue("project")
	taskID := r.PathValue("task")

	note, body, err := st.ReadTask(projectID, taskID)
	if err != nil {
		s.fail(w, err, http.StatusNotFound)
		return
	}

	var since time.Time
	if note.ConsolidatedThrough != "" {
		if t, err := store.ParseTime(note.ConsolidatedThrough); err == nil {
			since = t
		}
	}
	unconsolidated, err := st.Entries(projectID, store.EntryFilter{Task: taskID, Since: since})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	all, err := st.Entries(projectID, store.EntryFilter{Task: taskID})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	graph, err := st.DependencyGraph(projectID)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	s.render(w, "task.html", map[string]any{
		"Page":           "projects",
		"ProjectID":      projectID,
		"Task":           note,
		"Body":           markdown(body),
		"HasBody":        hasContent(body),
		"Unconsolidated": decorate(unconsolidated, cfg.Stale.MaxAgeDays),
		"Folded":         len(all) - len(unconsolidated),
		"OpenBlockers":   graph.OpenBlockers(taskID),
		"Blockers":       graph.BlockedBy[taskID],
		"Blocks":         graph.Blocks[taskID],
		"Closed":         graph.Closed[taskID],
		"Version":        s.version,
	})
}

// handleTaskRedirect resolves a bare task id to its project.
func (s *Server) handleTaskRedirect(w http.ResponseWriter, r *http.Request) {
	st, _, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	taskID := r.PathValue("task")

	project, err := st.FindTask(taskID)
	if err != nil {
		s.fail(w, err, http.StatusNotFound)
		return
	}
	http.Redirect(w, r, "/project/"+project+"/task/"+taskID, http.StatusSeeOther)
}

func (s *Server) handleRepoNote(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	projectID := r.PathValue("project")
	repo := r.PathValue("repo")

	note, body, err := st.ReadRepoNote(projectID, repo)
	if err != nil {
		note = &store.RepoNote{Repo: repo, Project: projectID}
		body = ""
	}
	findings, err := st.Entries(projectID, store.EntryFilter{
		Scope: store.ScopeRepo, Repos: []string{repo}, Kinds: []string{store.KindFinding},
	})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	s.render(w, "repo.html", map[string]any{
		"Page":      "projects",
		"ProjectID": projectID,
		"Note":      note,
		"Repo":      repo,
		"Body":      markdown(body),
		"HasBody":   hasContent(body),
		"Findings":  decorate(findings, cfg.Stale.MaxAgeDays),
		"Version":   s.version,
	})
}

// handleSubjectNote is a project's page about an environment or a service, the
// counterpart of handleRepoNote.
//
// Both ids come from the URL and become path segments, so they are validated
// here rather than trusting the read to refuse them.
func (s *Server) handleSubjectNote(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, cfg, err := s.newStore()
		if err != nil {
			s.fail(w, err, http.StatusInternalServerError)
			return
		}
		projectID := r.PathValue("project")
		sub := store.Subject{Kind: kind, ID: r.PathValue("id")}
		if err := store.ValidateID("project", projectID); err != nil {
			s.fail(w, err, http.StatusBadRequest)
			return
		}
		if err := store.ValidateSubject(sub.Kind, sub.ID); err != nil {
			s.fail(w, err, http.StatusBadRequest)
			return
		}
		if _, _, err := st.ReadProject(projectID); err != nil {
			s.fail(w, err, http.StatusNotFound)
			return
		}

		_, body, err := st.ReadSubjectNote(projectID, sub)
		if err != nil {
			body = ""
		}
		findings, err := st.Entries(projectID, store.EntryFilter{
			Scope: kind, Subjects: []string{sub.ID}, Kinds: []string{store.KindFinding},
		})
		if err != nil {
			s.fail(w, err, http.StatusInternalServerError)
			return
		}

		s.render(w, "subject.html", map[string]any{
			"Page":      "projects",
			"ProjectID": projectID,
			"Kind":      kind,
			"Noun":      subjectNouns[kind],
			"ID":        sub.ID,
			"Body":      markdown(body),
			"HasBody":   hasContent(body),
			"Findings":  decorate(findings, cfg.Stale.MaxAgeDays),
			"Version":   s.version,
		})
	}
}

// handleLog is the cross-cutting view: every entry, filtered by anything.
//
// The reason this interface exists at all. Obsidian reads one note at a time
// and a Bases view filters awkwardly across folders; this is where "which hosts
// actually wrote anything" gets answered.
func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	want := struct{ Project, Task, Kind, Host, Scope, Repo, Environment, Service string }{
		Project: q.Get("project"), Task: q.Get("task"), Kind: q.Get("kind"),
		Host: q.Get("host"), Scope: q.Get("scope"), Repo: q.Get("repo"),
		Environment: q.Get("environment"), Service: q.Get("service"),
	}

	projects, err := st.ListProjects()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	var entries []*store.Entry
	kinds := map[string]int{}
	hosts := map[string]int{}
	scopes := map[string]int{}

	for _, p := range projects {
		if want.Project != "" && p != want.Project {
			continue
		}
		// The log is the history view, so replaced entries stay, marked as such.
		found, err := st.Entries(p, store.EntryFilter{IncludeSuperseded: true})
		if err != nil {
			continue
		}
		for _, e := range found {
			// Counts describe the unfiltered set for this project selection, so
			// the facets do not vanish as soon as one is chosen.
			kinds[e.Kind]++
			hosts[e.Host]++
			scopes[e.Scope]++

			switch {
			case want.Task != "" && e.Task != want.Task:
			case want.Kind != "" && e.Kind != want.Kind:
			case want.Host != "" && e.Host != want.Host:
			case want.Scope != "" && e.Scope != want.Scope:
			case want.Repo != "" && e.Repo != want.Repo:
			// Case-folded, as the store matches subjects: the graph node these
			// links come from joins every spelling of one name.
			case want.Environment != "" && !strings.EqualFold(e.Environment, want.Environment):
			case want.Service != "" && !strings.EqualFold(e.Service, want.Service):
			default:
				entries = append(entries, e)
			}
		}
	}

	// Newest first: a log is read from the end.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time().After(entries[j].Time()) })

	total := len(entries)
	if len(entries) > 300 {
		entries = entries[:300]
	}

	s.render(w, "log.html", map[string]any{
		"Page":     "log",
		"Entries":  decorate(entries, cfg.Stale.MaxAgeDays),
		"Total":    total,
		"Shown":    len(entries),
		"Projects": projects,
		"Kinds":    facet(kinds),
		"Hosts":    facet(hosts),
		"Scopes":   facet(scopes),
		"Want":     want,
		"Version":  s.version,
	})
}

func (s *Server) handleNewProjectForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, "new.html", map[string]any{
		"Page":    "new",
		"Version": s.version,
	})
}

func (s *Server) handleNewProject(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, err, http.StatusBadRequest)
		return
	}

	svc, err := s.newService()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	req := service.OpenProjectRequest{
		ID:               strings.TrimSpace(r.PostFormValue("project_id")),
		Name:             strings.TrimSpace(r.PostFormValue("name")),
		JiraProjects:     splitList(r.PostFormValue("jira_projects")),
		GitLabGroups:     splitList(r.PostFormValue("gitlab_groups")),
		Repos:            splitList(r.PostFormValue("repos")),
		ConfluenceSpaces: splitList(r.PostFormValue("confluence_spaces")),
	}

	if _, err := svc.OpenProject(req); err != nil {
		s.render(w, "new.html", map[string]any{
			"Page":    "new",
			"Error":   err.Error(),
			"Form":    req,
			"Version": s.version,
		})
		return
	}
	http.Redirect(w, r, "/project/"+req.ID, http.StatusSeeOther)
}

// splitList accepts one per line or comma-separated, since a form invites both.
func splitList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	})
	var out []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func lastN[T any](items []T, n int) []T {
	if len(items) <= n {
		return items
	}
	return items[len(items)-n:]
}

type facetValue struct {
	Value string
	Count int
}

func facet(counts map[string]int) []facetValue {
	out := make([]facetValue, 0, len(counts))
	for v, c := range counts {
		if v == "" {
			continue
		}
		out = append(out, facetValue{Value: v, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// handleChecks lists what store.Check finds, grouped by project.
//
// The checks are advice, never enforcement: each one is something a person
// decides, such as whether a repo belongs on the project note. This page is
// where the decision gets made.
func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	st, _, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	issues, err := st.Check()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}

	type group struct {
		Project string
		Issues  []store.Issue
	}
	var groups []group
	index := map[string]int{}
	warn := 0
	for _, i := range issues {
		if i.Severity == store.SeverityWarn {
			warn++
		}
		at, ok := index[i.Project]
		if !ok {
			at = len(groups)
			index[i.Project] = at
			groups = append(groups, group{Project: i.Project})
		}
		groups[at].Issues = append(groups[at].Issues, i)
	}
	// The vault-wide group first, then projects alphabetically.
	sort.SliceStable(groups, func(i, j int) bool {
		if (groups[i].Project == "") != (groups[j].Project == "") {
			return groups[i].Project == ""
		}
		return groups[i].Project < groups[j].Project
	})

	s.render(w, "checks.html", map[string]any{
		"Page":    "checks",
		"Groups":  groups,
		"Total":   len(issues),
		"Warn":    warn,
		"Version": s.version,
	})
}

// projectIssues returns the checks that concern one project.
func projectIssues(st *store.Store, project string) []store.Issue {
	issues, err := st.Check()
	if err != nil {
		return nil
	}
	var out []store.Issue
	for _, i := range issues {
		if i.Project == project {
			out = append(out, i)
		}
	}
	return out
}
