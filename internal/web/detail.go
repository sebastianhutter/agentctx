package web

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// detailEntries caps the entries a panel shows. The panel is for orienting;
// the task page and the log have the rest.
const detailEntries = 12

// kindCount is one row of an entries-by-kind breakdown.
type kindCount struct {
	Kind  string
	Count int
}

// countKinds breaks entries down by kind, in the order a reader cares about.
func countKinds(entries []*store.Entry) []kindCount {
	order := []string{store.KindFinding, store.KindDecision, store.KindNote,
		store.KindDependency, store.KindOpened, store.KindClosed}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Kind]++
	}
	var out []kindCount
	for _, k := range order {
		if counts[k] > 0 {
			out = append(out, kindCount{k, counts[k]})
		}
	}
	return out
}

// newestFirst returns up to n entries, newest first, leaving out the opened
// and closed markers: the panel already states a task's state.
func newestFirst(entries []*store.Entry, n int) []*store.Entry {
	var out []*store.Entry
	for i := len(entries) - 1; i >= 0 && len(out) < n; i-- {
		switch entries[i].Kind {
		case store.KindOpened, store.KindClosed:
			continue
		}
		out = append(out, entries[i])
	}
	return out
}

// handleGraphNode renders the side panel for one graph node as an HTML
// fragment.
//
// Server-rendered rather than assembled in the browser from graph.json, so the
// panel shows entries exactly as every other page does — the same markdown
// rendering, the same staleness marks — and graph.json stays small enough to
// load the whole vault at once.
func (s *Server) handleGraphNode(w http.ResponseWriter, r *http.Request) {
	st, cfg, err := s.newStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	project := q.Get("project")
	if project != "" {
		if err := store.ValidateID("project", project); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	var name string
	var data map[string]any
	switch q.Get("kind") {
	case nodeProject:
		name, data, err = projectDetail(st, cfg, project)
	case nodeTask:
		name, data, err = taskDetail(st, cfg, project, q.Get("task"))
	case nodeRepo, nodeEnvironment, nodeService:
		kind := q.Get("kind")
		name, data, err = subjectDetail(st, cfg, store.Subject{Kind: kind, ID: q.Get(kind)})
	case nodeEntry:
		name, data, err = entryDetail(st, cfg, project, q.Get("file"))
	default:
		err = fmt.Errorf("unknown kind %q", q.Get("kind"))
	}
	if err != nil {
		code := http.StatusNotFound
		if errors.Is(err, errBadRequest) {
			code = http.StatusBadRequest
		}
		http.Error(w, err.Error(), code)
		return
	}

	var buf bytes.Buffer
	if err := s.detail.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

var errBadRequest = errors.New("bad request")

func projectDetail(st *store.Store, cfg *config.Config, id string) (string, map[string]any, error) {
	note, body, err := st.ReadProject(id)
	if err != nil {
		return "", nil, err
	}
	entries, err := st.Entries(id, store.EntryFilter{})
	if err != nil {
		return "", nil, err
	}
	deps, err := st.DependencyGraph(id)
	if err != nil {
		return "", nil, err
	}
	taskIDs, err := st.ListTasks(id)
	if err != nil {
		return "", nil, err
	}

	var ready, blocked, closed []string
	for _, t := range taskIDs {
		switch {
		case deps.Closed[t]:
			closed = append(closed, t)
		case deps.Ready(t):
			ready = append(ready, t)
		default:
			blocked = append(blocked, t)
		}
	}
	var last *store.Entry
	if len(entries) > 0 {
		last = entries[len(entries)-1]
	}

	return "detail-project", map[string]any{
		"Project":      note,
		"Consolidated": hasContent(body),
		"Ready":        ready,
		"Blocked":      blocked,
		"Closed":       closed,
		"Kinds":        countKinds(entries),
		"Last":         last,
		"Issues":       projectIssues(st, id),
		"Recent":       decorate(newestFirst(entries, detailEntries), cfg.Stale.MaxAgeDays),
	}, nil
}

func taskDetail(st *store.Store, cfg *config.Config, project, id string) (string, map[string]any, error) {
	note, body, err := st.ReadTask(project, id)
	if err != nil {
		return "", nil, err
	}
	entries, err := st.Entries(project, store.EntryFilter{Task: id})
	if err != nil {
		return "", nil, err
	}
	deps, err := st.DependencyGraph(project)
	if err != nil {
		return "", nil, err
	}

	// Repos the task has actually written about, beside the ones its note
	// lists: the difference is often the interesting part.
	about := map[string]int{}
	hosts := map[string]bool{}
	for _, e := range entries {
		if e.Repo != "" {
			about[e.Repo]++
		}
		if e.Host != "" {
			hosts[e.Host] = true
		}
	}
	type repoRow struct {
		Repo    string
		Listed  bool
		Entries int
	}
	var repos []repoRow
	for _, r := range note.Repos {
		repos = append(repos, repoRow{r, true, about[r]})
		delete(about, r)
	}
	for _, r := range sortedKeys(about) {
		repos = append(repos, repoRow{r, false, about[r]})
	}

	var opened, last *store.Entry
	for _, e := range entries {
		if opened == nil && e.Kind == store.KindOpened {
			opened = e
		}
		last = e
	}

	state := "ready"
	switch {
	case deps.Closed[id]:
		state = "closed"
	case !deps.Ready(id):
		state = "blocked"
	}

	type depRow struct {
		ID     string
		Closed bool
	}
	rows := func(ids []string) []depRow {
		out := make([]depRow, 0, len(ids))
		for _, d := range ids {
			out = append(out, depRow{d, deps.Closed[d]})
		}
		return out
	}

	var issues []store.Issue
	for _, i := range projectIssues(st, project) {
		if i.Subject == id {
			issues = append(issues, i)
		}
	}

	return "detail-task", map[string]any{
		"ProjectID":    project,
		"Task":         note,
		"State":        state,
		"Consolidated": hasContent(body),
		"Body":         markdown(body),
		"WaitsFor":     rows(deps.BlockedBy[id]),
		"Blocks":       rows(deps.Blocks[id]),
		"Repos":        repos,
		"Kinds":        countKinds(entries),
		"Opened":       opened,
		"Last":         last,
		"Hosts":        sortedKeys(hosts),
		"Issues":       issues,
		"Recent":       decorate(newestFirst(entries, detailEntries), cfg.Stale.MaxAgeDays),
		"More":         len(entries) > detailEntries,
	}, nil
}

// subjectNouns name each subject kind the way a heading says it.
var subjectNouns = map[string]string{
	store.ScopeRepo:        "repository",
	store.ScopeEnvironment: "environment",
	store.ScopeService:     "service",
}

// subjectDetail is the panel for a repo, an environment or a service.
//
// The id arrives from a URL and becomes a file name on the way to the subject's
// page, so it is validated before anything reads the vault with it.
func subjectDetail(st *store.Store, cfg *config.Config, sub store.Subject) (string, map[string]any, error) {
	if err := store.ValidateSubject(sub.Kind, sub.ID); err != nil {
		return "", nil, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	ids, err := st.ListProjects()
	if err != nil {
		return "", nil, err
	}

	// One row per project that touches the subject, since knowledge about one
	// is kept per project and this is the one place it is seen whole.
	type projectRow struct {
		Project      string
		Listed       bool
		Consolidated bool
		Findings     int
		Tasks        []string
	}
	// A repo is mentioned by any entry naming it; the other kinds only by the
	// entries scoped to them, which is all that can name them.
	filter := store.EntryFilter{Scope: sub.Kind, Subjects: []string{sub.ID}}
	if sub.Kind == store.ScopeRepo {
		filter = store.EntryFilter{Repos: []string{sub.ID}}
	}
	var rows []projectRow
	var findings []*store.Entry
	for _, pid := range ids {
		note, _, err := st.ReadProject(pid)
		if err != nil {
			continue
		}
		row := projectRow{Project: pid}
		for _, r := range note.SubjectsOf(sub.Kind) {
			if strings.EqualFold(r, sub.ID) {
				row.Listed = true
			}
		}
		row.Consolidated = subjectHasBody(st, pid, sub)
		// Only a task note lists repos; for the other kinds the tasks come
		// from the entries alone.
		if sub.Kind == store.ScopeRepo {
			taskIDs, err := st.ListTasks(pid)
			if err != nil {
				return "", nil, err
			}
			for _, t := range taskIDs {
				tn, _, err := st.ReadTask(pid, t)
				if err != nil {
					continue
				}
				for _, r := range tn.Repos {
					if strings.EqualFold(r, sub.ID) {
						row.Tasks = appendUnique(row.Tasks, t)
					}
				}
			}
		}
		entries, err := st.Entries(pid, filter)
		if err != nil {
			return "", nil, err
		}
		for _, e := range entries {
			if e.Task != "" {
				row.Tasks = appendUnique(row.Tasks, e.Task)
			}
			if e.Kind == store.KindFinding && e.Scope == sub.Kind {
				row.Findings++
				findings = append(findings, e)
			}
		}
		if row.Listed || row.Findings > 0 || len(row.Tasks) > 0 || len(entries) > 0 {
			sort.Strings(row.Tasks)
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return "", nil, fmt.Errorf("no project mentions %q", sub.ID)
	}

	sort.SliceStable(findings, func(i, j int) bool { return findings[i].Time().Before(findings[j].Time()) })

	var issues []store.Issue
	if all, err := st.Check(); err == nil {
		for _, i := range all {
			if strings.EqualFold(i.Subject, sub.ID) {
				issues = append(issues, i)
			}
		}
	}

	return "detail-subject", map[string]any{
		"Kind":     sub.Kind,
		"Noun":     subjectNouns[sub.Kind],
		"ID":       sub.ID,
		"Projects": rows,
		"Findings": len(findings),
		"Issues":   issues,
		"Recent":   decorate(newestFirst(findings, detailEntries), cfg.Stale.MaxAgeDays),
	}, nil
}

// subjectHasBody reports whether a project's page about a subject has been
// consolidated into something worth reading.
func subjectHasBody(st *store.Store, project string, sub store.Subject) bool {
	var body string
	var err error
	if sub.Kind == store.ScopeRepo {
		_, body, err = st.ReadRepoNote(project, sub.ID)
	} else {
		_, body, err = st.ReadSubjectNote(project, sub)
	}
	return err == nil && hasContent(body)
}

func entryDetail(st *store.Store, cfg *config.Config, project, file string) (string, map[string]any, error) {
	if file == "" || filepath.Base(file) != file || !strings.HasSuffix(file, ".md") {
		return "", nil, fmt.Errorf("%w: %q is not an entry file name", errBadRequest, file)
	}
	entries, err := st.Entries(project, store.EntryFilter{})
	if err != nil {
		return "", nil, err
	}
	for _, e := range entries {
		if filepath.Base(e.Path) == file {
			return "detail-entry", map[string]any{
				"ProjectID": project,
				"Entry":     decorate([]*store.Entry{e}, cfg.Stale.MaxAgeDays),
				"File":      file,
			}, nil
		}
	}
	return "", nil, fmt.Errorf("no entry %s in %s", file, project)
}

func appendUnique(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ageOf renders how long ago a timestamp was, for the panel's "last activity".
func ageOf(ts string) string {
	t, err := store.ParseTime(ts)
	if err != nil {
		return ""
	}
	return humanAge(time.Since(t))
}

// parseDetail builds the template set for the panel fragments.
func parseDetail() (*template.Template, error) {
	f := funcs()
	f["age"] = ageOf
	return template.New("detail").Funcs(f).ParseFS(assets,
		"templates/entrylist.html", "templates/issuelist.html", "templates/detail.html")
}
