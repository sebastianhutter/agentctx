package store

import (
	"fmt"
	"sort"
	"strings"
)

// DependencyGraph is the current ordering between a project's tasks, folded out
// of the log.
//
// Derived, never stored. A `blocked_by:` list in a task note would be a field
// that goes stale the moment a blocker is finished and nobody edits the note —
// the drift that keeps status out of these notes in the first place. Folding
// the log instead means a dependency is a fact with a time and an author, a
// resolution is another fact, and the order they happened in survives.
type DependencyGraph struct {
	// BlockedBy maps a task to what it is waiting for.
	BlockedBy map[string][]string

	// Blocks is the reverse: what waits for this task.
	Blocks map[string][]string

	// Closed marks tasks that have been closed, since a closed blocker does not
	// block anything.
	Closed map[string]bool
}

// DependencyGraph folds a project's dependency entries into the current graph.
func (s *Store) DependencyGraph(project string) (*DependencyGraph, error) {
	entries, err := s.Entries(project, EntryFilter{})
	if err != nil {
		return nil, err
	}

	// edges[task][dependency] — last entry wins, which is what makes an
	// append-only log able to express a change of mind.
	edges := map[string]map[string]bool{}
	closed := map[string]bool{}

	for _, e := range entries {
		if e.Kind == KindClosed && e.Task != "" {
			closed[e.Task] = true
		}
		if e.Kind != KindDependency || e.Task == "" {
			continue
		}
		if edges[e.Task] == nil {
			edges[e.Task] = map[string]bool{}
		}
		for _, dep := range e.DependsOn {
			edges[e.Task][dep] = !e.Resolved
		}
	}

	g := &DependencyGraph{
		BlockedBy: map[string][]string{},
		Blocks:    map[string][]string{},
		Closed:    closed,
	}
	for task, deps := range edges {
		for dep, active := range deps {
			if !active {
				continue
			}
			g.BlockedBy[task] = append(g.BlockedBy[task], dep)
			g.Blocks[dep] = append(g.Blocks[dep], task)
		}
	}
	for _, list := range g.BlockedBy {
		sort.Strings(list)
	}
	for _, list := range g.Blocks {
		sort.Strings(list)
	}
	return g, nil
}

// OpenBlockers returns the tasks still standing in this one's way.
//
// A closed blocker is not a blocker. That is the whole reason the graph is
// derived rather than stored: nobody has to remember to clear the edge.
func (g *DependencyGraph) OpenBlockers(task string) []string {
	var open []string
	for _, dep := range g.BlockedBy[task] {
		if !g.Closed[dep] {
			open = append(open, dep)
		}
	}
	return open
}

// Ready reports whether a task can be started now.
func (g *DependencyGraph) Ready(task string) bool {
	return len(g.OpenBlockers(task)) == 0
}

// WouldCycle returns the path proving that making task depend on dep closes a
// loop, or nil when it is safe.
//
// Worth refusing rather than recording: a cycle makes "which of these can I
// start" unanswerable, and it is far easier to reject the edge that creates one
// than to explain the resulting deadlock weeks later.
func (g *DependencyGraph) WouldCycle(task, dep string) []string {
	if task == dep {
		return []string{task, dep}
	}

	// A cycle appears exactly when dep already reaches task.
	type step struct {
		node string
		path []string
	}
	seen := map[string]bool{dep: true}
	queue := []step{{node: dep, path: []string{task, dep}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		for _, next := range g.BlockedBy[cur.node] {
			if next == task {
				return append(append([]string{}, cur.path...), task)
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, step{node: next, path: append(append([]string{}, cur.path...), next)})
		}
	}
	return nil
}

// FormatCycle renders a cycle path for an error message.
func FormatCycle(path []string) string {
	return strings.Join(path, " → ")
}

// DescribeBlockers renders the blockers of a task, noting which are closed.
func (g *DependencyGraph) DescribeBlockers(task string) string {
	deps := g.BlockedBy[task]
	if len(deps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(deps))
	for _, dep := range deps {
		if g.Closed[dep] {
			parts = append(parts, fmt.Sprintf("%s (closed)", dep))
			continue
		}
		parts = append(parts, dep)
	}
	return strings.Join(parts, ", ")
}
