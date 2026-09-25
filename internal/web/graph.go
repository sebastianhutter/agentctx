package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// Node kinds in the graph.
const (
	nodeProject = "project"
	nodeTask    = "task"
	nodeRepo    = "repo"
	nodeEntry   = "entry"
)

// Edge relations in the graph.
const (
	relIn     = "in"      // task → project
	relOwns   = "owns"    // project → repo, listed on the project note
	relUses   = "uses"    // project → repo, written about but not listed
	relTouch  = "touches" // task → repo
	relWaits  = "waits"   // task → the task it waits for, still open
	relWaited = "waited"  // task → a blocker that has since closed
	relLogged = "logged"  // entry → its task, or its project
	relAbout  = "about"   // entry → the repo it is about
)

// graphNode is one node as the page draws it.
type graphNode struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
	// State is the thing a colour encodes: ready/blocked/closed for a task,
	// the scope for an entry, consolidated/unconsolidated for a repo.
	State  string `json:"state,omitempty"`
	Detail string `json:"detail,omitempty"`
	URL    string `json:"url,omitempty"`
	// Weight sizes the node: entries for a project or task, findings for a
	// repo.
	Weight int    `json:"weight"`
	TS     string `json:"ts,omitempty"`
}

// graphEdge is a directed relation between two nodes.
type graphEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Rel    string `json:"rel"`
}

// graphData is what /graph.json returns.
type graphData struct {
	Nodes    []graphNode `json:"nodes"`
	Edges    []graphEdge `json:"edges"`
	Projects []string    `json:"projects"`
}

// graphBuilder accumulates nodes and edges without duplicates.
type graphBuilder struct {
	nodes map[string]*graphNode
	order []string
	edges map[graphEdge]bool
}

func (b *graphBuilder) node(n graphNode) *graphNode {
	if have, ok := b.nodes[n.ID]; ok {
		return have
	}
	b.nodes[n.ID] = &n
	b.order = append(b.order, n.ID)
	return &n
}

func (b *graphBuilder) edge(source, target, rel string) {
	if source == target {
		return
	}
	b.edges[graphEdge{Source: source, Target: target, Rel: rel}] = true
}

func projectNodeID(p string) string     { return "project:" + p }
func taskNodeID(p, t string) string     { return "task:" + p + "/" + t }
func repoNodeID(repo string) string     { return "repo:" + strings.ToLower(repo) }
func entryNodeID(e *store.Entry) string { return "entry:" + filepath.Base(e.Path) }

// buildGraph folds the vault into nodes and edges.
//
// Repos are keyed by name alone, not by project: the point of drawing this at
// all is that one repository worked on from two projects shows up as one node
// joining them, which neither Obsidian's graph nor a per-project page can show.
// A project's own repo notes stay per project; the node links to the log
// filtered by repo, which crosses projects.
//
// The opened and closed entries are left out of the entry nodes, since the
// task's state already says what they say, and so are dependency entries,
// which become task → task edges instead.
func buildGraph(st *store.Store, only string) (*graphData, error) {
	ids, err := st.ListProjects()
	if err != nil {
		return nil, err
	}

	b := &graphBuilder{nodes: map[string]*graphNode{}, edges: map[graphEdge]bool{}}
	repoProjects := map[string]map[string]bool{}
	repoConsolidated := map[string]bool{}

	for _, pid := range ids {
		if only != "" && pid != only {
			continue
		}
		note, body, err := st.ReadProject(pid)
		if err != nil {
			continue
		}
		entries, err := st.Entries(pid, store.EntryFilter{})
		if err != nil {
			return nil, err
		}
		deps, err := st.DependencyGraph(pid)
		if err != nil {
			return nil, err
		}
		taskIDs, err := st.ListTasks(pid)
		if err != nil {
			return nil, err
		}

		pnode := projectNodeID(pid)
		state := "consolidated"
		if !hasContent(body) {
			state = "unconsolidated"
		}
		p := b.node(graphNode{
			// Labelled by id: names are sentences, and ten of them overlap.
			ID: pnode, Kind: nodeProject, Label: pid, Title: note.Name, Project: pid,
			State: state, URL: "/project/" + url.PathEscape(pid),
		})
		p.Weight = len(entries)
		if len(entries) > 0 {
			p.TS = entries[len(entries)-1].TS
		}

		addRepo := func(repo string) string {
			key := strings.ToLower(repo)
			if repoProjects[key] == nil {
				repoProjects[key] = map[string]bool{}
			}
			repoProjects[key][pid] = true
			if _, _, err := st.ReadRepoNote(pid, repo); err == nil {
				repoConsolidated[key] = true
			}
			return b.node(graphNode{
				ID: repoNodeID(repo), Kind: nodeRepo, Label: repo,
				URL: "/log?repo=" + url.QueryEscape(repo),
			}).ID
		}

		listed := map[string]bool{}
		for _, repo := range note.Repos {
			listed[strings.ToLower(repo)] = true
			b.edge(pnode, addRepo(repo), relOwns)
		}
		usedBy := func(repo string) {
			if !listed[strings.ToLower(repo)] {
				b.edge(pnode, addRepo(repo), relUses)
			}
		}

		counts := map[string]int{}
		for _, e := range entries {
			if e.Task != "" {
				counts[e.Task]++
			}
		}

		for _, tid := range taskIDs {
			tn, _, err := st.ReadTask(pid, tid)
			if err != nil {
				continue
			}
			state := "ready"
			switch {
			case deps.Closed[tid]:
				state = "closed"
			case !deps.Ready(tid):
				state = "blocked"
			}
			tnode := b.node(graphNode{
				ID: taskNodeID(pid, tid), Kind: nodeTask, Label: tid, Title: tn.Title, Project: pid,
				State: state, Weight: counts[tid], Detail: tn.SourceKey,
				URL: "/project/" + url.PathEscape(pid) + "/task/" + url.PathEscape(tid),
			})
			b.edge(tnode.ID, pnode, relIn)
			for _, repo := range tn.Repos {
				usedBy(repo)
				b.edge(tnode.ID, repoNodeID(repo), relTouch)
			}
		}

		for task, blockers := range deps.BlockedBy {
			for _, dep := range blockers {
				rel := relWaits
				if deps.Closed[dep] {
					rel = relWaited
				}
				from, to := taskNodeID(pid, task), taskNodeID(pid, dep)
				if b.nodes[from] != nil && b.nodes[to] != nil {
					b.edge(from, to, rel)
				}
			}
		}

		for _, e := range entries {
			// The task an entry names may have no note (see the checks page);
			// fall back to the project rather than drawing an edge to nothing.
			owner := pnode
			if e.Task != "" && b.nodes[taskNodeID(pid, e.Task)] != nil {
				owner = taskNodeID(pid, e.Task)
			}
			if e.Repo != "" {
				usedBy(e.Repo)
				if owner != pnode {
					b.edge(owner, repoNodeID(e.Repo), relTouch)
				}
				if e.Scope == store.ScopeRepo && e.Kind == store.KindFinding {
					b.nodes[repoNodeID(e.Repo)].Weight++
				}
			}

			switch e.Kind {
			case store.KindOpened, store.KindClosed, store.KindDependency:
				continue
			}
			if e.Consolidation {
				continue
			}
			detail := e.Scope
			if e.Source != nil {
				detail += " · " + e.Source.Kind
				if e.Source.Ref != "" {
					detail += " " + e.Source.Ref
				}
			}
			if e.Confidence != "" {
				detail += " · " + e.Confidence
			}
			logURL := "/log?project=" + url.QueryEscape(pid)
			if e.Task != "" {
				logURL += "&task=" + url.QueryEscape(e.Task)
			}
			enode := b.node(graphNode{
				ID: entryNodeID(e), Kind: nodeEntry, Label: e.Kind, Title: entryTitle(e.Body),
				Project: pid, State: e.Scope, Detail: detail, URL: logURL, Weight: 1, TS: e.TS,
			})
			b.edge(enode.ID, owner, relLogged)
			if e.Repo != "" {
				b.edge(enode.ID, repoNodeID(e.Repo), relAbout)
			}
		}
	}

	for key, projects := range repoProjects {
		n := b.nodes["repo:"+key]
		names := make([]string, 0, len(projects))
		for p := range projects {
			names = append(names, p)
		}
		sort.Strings(names)
		n.Detail = strings.Join(names, ", ")
		n.State = "unconsolidated"
		if repoConsolidated[key] {
			n.State = "consolidated"
		}
		if len(names) > 1 {
			n.State = "shared-" + n.State
		}
	}

	out := &graphData{Projects: ids}
	for _, id := range b.order {
		out.Nodes = append(out.Nodes, *b.nodes[id])
	}
	for e := range b.edges {
		out.Edges = append(out.Edges, e)
	}
	sort.Slice(out.Edges, func(i, j int) bool {
		a, c := out.Edges[i], out.Edges[j]
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Target != c.Target {
			return a.Target < c.Target
		}
		return a.Rel < c.Rel
	})
	return out, nil
}

// entryTitle is the first line of an entry, short enough for a tooltip.
func entryTitle(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	body = strings.Trim(body, "*# ")
	if r := []rune(body); len(r) > 200 {
		body = string(r[:200]) + "…"
	}
	return body
}

func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	st, _, err := s.newStore()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	projects, err := st.ListProjects()
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError)
		return
	}
	s.render(w, "graph.html", map[string]any{
		"Page":     "graph",
		"Projects": projects,
		"Project":  r.URL.Query().Get("project"),
		"Version":  s.version,
	})
}

func (s *Server) handleGraphJSON(w http.ResponseWriter, r *http.Request) {
	st, _, err := s.newStore()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	only := r.URL.Query().Get("project")
	if only != "" {
		if err := store.ValidateID("project", only); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	data, err := buildGraph(st, only)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
