package store

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Issue severities.
const (
	// SeverityWarn is something that already makes a lookup miss or split one
	// thing's knowledge in two.
	SeverityWarn = "warn"

	// SeverityInfo is worth knowing and cheap to fix, but loses nothing today.
	SeverityInfo = "info"
)

// Issue codes. Stable, so a caller can filter or count them.
const (
	IssueRepoUnlisted      = "repo-unlisted"
	IssueRepoNearDuplicate = "repo-near-duplicate"
	IssueKeyUnmapped       = "key-unmapped"
	IssueKeyClaimedTwice   = "key-claimed-twice"
	IssueEntryOrphan       = "entry-orphan"
	IssueEntryMisfiled     = "entry-misfiled"
	IssueDependencyUnknown = "dependency-unknown"
	IssueTaskNothingLogged = "task-nothing-logged"
	IssueBacklog           = "consolidation-backlog"
)

// BacklogThreshold is how many unconsolidated entries a note may carry before
// it is reported. Skipping consolidation never loses a fact, so this is about
// how much an agent has to read, not about correctness.
const BacklogThreshold = 15

// Issue is one thing in the vault that is not wrong enough to refuse, but
// quietly makes the knowledge in it harder to find.
type Issue struct {
	Severity string
	Code     string
	Project  string // empty for an issue across projects
	Subject  string // the task, repo or key it is about
	Message  string
	Fix      string
}

// taskKey matches a task id that looks like a tracker key, proj-142.
var taskKey = regexp.MustCompile(`^([a-z][a-z0-9]*)-\d+$`)

// projectData is what the checks read about one project, loaded once.
type projectData struct {
	id      string
	note    *ProjectNote
	body    string
	tasks   map[string]*TaskNote
	entries []*Entry
}

// Check reads the whole vault and reports what makes its knowledge harder to
// reach.
//
// Nothing here is enforced at write time, on purpose. Each of these is a
// judgement a person makes, since a repo outside the project's list may be a
// one-off rather than an omission. What the checks can do is make the drift
// visible before it compounds. A repo left off the project note, for example,
// means its findings never reach an agent that opens the project.
func (s *Store) Check() ([]Issue, error) {
	ids, err := s.ListProjects()
	if err != nil {
		return nil, err
	}

	var projects []*projectData
	for _, id := range ids {
		note, body, err := s.ReadProject(id)
		if err != nil {
			continue
		}
		p := &projectData{id: id, note: note, body: body, tasks: map[string]*TaskNote{}}
		taskIDs, err := s.ListTasks(id)
		if err != nil {
			return nil, err
		}
		for _, t := range taskIDs {
			if tn, _, err := s.ReadTask(id, t); err == nil {
				p.tasks[t] = tn
			}
		}
		if p.entries, err = s.Entries(id, EntryFilter{}); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}

	var issues []Issue
	for _, p := range projects {
		issues = append(issues, checkProject(p)...)
	}
	issues = append(issues, checkKeyClaims(projects)...)
	issues = append(issues, checkUnmappedKeys(projects)...)
	issues = append(issues, checkRepoNames(projects)...)

	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Severity != issues[j].Severity {
			return issues[i].Severity == SeverityWarn
		}
		if issues[i].Project != issues[j].Project {
			return issues[i].Project < issues[j].Project
		}
		return issues[i].Code < issues[j].Code
	})
	return issues, nil
}

func checkProject(p *projectData) []Issue {
	var issues []Issue
	add := func(sev, code, subject, msg, fix string) {
		issues = append(issues, Issue{Severity: sev, Code: code, Project: p.id, Subject: subject, Message: msg, Fix: fix})
	}

	// Repos in use but missing from the project note. The note's list decides
	// which repo notes a project bundle reads, so the findings about such a
	// repo exist and are never shown.
	listed := map[string]bool{}
	for _, r := range p.note.Repos {
		listed[strings.ToLower(r)] = true
	}
	unlisted := map[string][]string{}
	unlistedEntries := map[string]int{}
	for _, id := range sortedKeys(p.tasks) {
		for _, r := range p.tasks[id].Repos {
			if !listed[strings.ToLower(r)] {
				unlisted[r] = appendOnce(unlisted[r], "task "+id)
			}
		}
	}
	for _, e := range p.entries {
		if e.Repo != "" && !listed[strings.ToLower(e.Repo)] {
			unlistedEntries[e.Repo]++
			if unlisted[e.Repo] == nil {
				unlisted[e.Repo] = []string{}
			}
		}
	}
	for _, r := range sortedKeys(unlisted) {
		users := unlisted[r]
		if n := unlistedEntries[r]; n > 0 {
			users = append(users, plural(n, "log entry"))
		}
		add(SeverityWarn, IssueRepoUnlisted, r,
			fmt.Sprintf("%s is used by %s but is not in the project's repos", r, strings.Join(users, " and ")),
			fmt.Sprintf("amend_project %s add_repos=[%s] if it belongs, or supersede the findings under the repo's canonical name", p.id, r))
	}

	// Entries that point at nothing, or at another project.
	orphans := map[string]int{}
	misfiled := 0
	for _, e := range p.entries {
		if e.Task != "" && p.tasks[e.Task] == nil {
			orphans[e.Task]++
		}
		if e.Project != "" && e.Project != p.id {
			misfiled++
		}
	}
	for _, t := range sortedKeys(orphans) {
		add(SeverityWarn, IssueEntryOrphan, t,
			fmt.Sprintf("%s %s task %s, which has no task note in this project", plural(orphans[t], "entry"), agree(orphans[t], "names", "name"), t),
			"open the task with open_task so its note exists, or check for a misspelt id")
	}
	if misfiled > 0 {
		add(SeverityWarn, IssueEntryMisfiled, "",
			fmt.Sprintf("%s in this folder %s to another project", plural(misfiled, "entry"), agree(misfiled, "belongs", "belong")),
			"move the files into the folder their project field names")
	}

	// Dependencies on tasks that do not exist here. Dependencies are
	// project-local, so such an edge never resolves and the waiting task is
	// blocked forever.
	for _, e := range p.entries {
		if e.Kind != KindDependency || e.Resolved {
			continue
		}
		for _, dep := range e.DependsOn {
			if p.tasks[dep] == nil {
				add(SeverityWarn, IssueDependencyUnknown, e.Task,
					fmt.Sprintf("%s waits for %s, which is not a task in this project, so it can never become ready", e.Task, dep),
					fmt.Sprintf("open %s in this project, or resolve the dependency", dep))
			}
		}
	}

	// Tasks that were opened and nothing else. Usually a session that forgot
	// to log, sometimes a task created speculatively and never started.
	logged := map[string]bool{}
	for _, e := range p.entries {
		if e.Task != "" && e.Kind != KindOpened {
			logged[e.Task] = true
		}
	}
	for _, id := range sortedKeys(p.tasks) {
		if !logged[id] {
			add(SeverityInfo, IssueTaskNothingLogged, id,
				fmt.Sprintf("task %s has been opened but nothing was logged on it", id),
				"log what was learned, or close it with a note that it was not started")
		}
	}

	// Consolidation backlog, per note. Never a correctness problem, but past a
	// point a bundle is a wall of entries rather than a summary.
	since := func(through string) time.Time {
		t, _ := ParseTime(through)
		return t
	}
	count := func(match func(*Entry) bool, through string) int {
		n, cut := 0, since(through)
		for _, e := range p.entries {
			if e.Kind == KindOpened || e.Consolidation || !match(e) {
				continue
			}
			if !cut.IsZero() && e.Time().Before(cut) {
				continue
			}
			n++
		}
		return n
	}
	if n := count(func(e *Entry) bool { return e.Task == "" || e.Scope == ScopeProject }, p.note.ConsolidatedThrough); n >= BacklogThreshold {
		add(SeverityInfo, IssueBacklog, p.id,
			fmt.Sprintf("the project note has %s not yet folded in", plural(n, "entry")),
			fmt.Sprintf("run consolidate on project %s", p.id))
	}
	for _, id := range sortedKeys(p.tasks) {
		if n := count(func(e *Entry) bool { return e.Task == id }, p.tasks[id].ConsolidatedThrough); n >= BacklogThreshold {
			add(SeverityInfo, IssueBacklog, id,
				fmt.Sprintf("task %s has %s not yet folded into its note", id, plural(n, "entry")),
				fmt.Sprintf("run consolidate on task %s", id))
		}
	}

	return issues
}

// checkKeyClaims reports a tracker key claimed by more than one project.
// Resolution takes the first project in directory order, so every task under
// that key lands in one of them and the other never gets any.
func checkKeyClaims(projects []*projectData) []Issue {
	claims := keyClaims(projects)
	jira := map[string]bool{}
	for _, p := range projects {
		for _, k := range p.note.JiraProjects {
			jira[strings.ToUpper(strings.TrimSpace(k))] = true
		}
	}
	var issues []Issue
	for _, k := range sortedKeys(claims) {
		if len(claims[k]) < 2 {
			continue
		}
		// A shared GitLab group is normal: two projects often live in one
		// group. It only matters for keys that are resolved, so it is info.
		sev := SeverityInfo
		if jira[k] {
			sev = SeverityWarn
		}
		issues = append(issues, Issue{
			Severity: sev, Code: IssueKeyClaimedTwice, Subject: k,
			Message: fmt.Sprintf("%s is claimed by %s; a new %s key always resolves to %s",
				k, strings.Join(claims[k], " and "), k, claims[k][0]),
			Fix: "keep the key on one project and pass project explicitly to open_task for the others",
		})
	}
	return issues
}

// keyClaims maps each claimed tracker key or group, upper case, to the
// projects claiming it, in directory order — the order resolution tries them.
func keyClaims(projects []*projectData) map[string][]string {
	claims := map[string][]string{}
	for _, p := range projects {
		if claims[strings.ToUpper(p.id)] == nil {
			// A project id is itself a prefix ResolveProjectForKey accepts.
			claims[strings.ToUpper(p.id)] = nil
		}
		for _, k := range append(append([]string{}, p.note.JiraProjects...), p.note.GitLabGroups...) {
			k = strings.ToUpper(strings.TrimSpace(k))
			if k != "" {
				claims[k] = appendOnce(claims[k], p.id)
			}
		}
	}
	return claims
}

// checkUnmappedKeys reports tracker-style task ids whose prefix the project
// they live in does not claim.
//
// Three different situations, with three different answers. The prefix is
// claimed by nobody and used by one project: claim it there, and open_task on
// the next key finds the project by itself. The prefix is used by several
// projects: one tracker spans them, no single claim is right, and passing
// project to open_task is the intended path. The prefix is claimed by another
// project: a new key resolves there, which is right only if the caller
// remembers to pass project for this one.
func checkUnmappedKeys(projects []*projectData) []Issue {
	claims := keyClaims(projects)

	// prefix -> project -> task ids
	unmapped := map[string]map[string][]string{}
	for _, p := range projects {
		for _, id := range sortedKeys(p.tasks) {
			t := p.tasks[id]
			if t.Source != "jira" && t.Source != "gitlab" && t.Source != "github" {
				continue
			}
			m := taskKey.FindStringSubmatch(id)
			if m == nil {
				continue
			}
			key := strings.ToUpper(m[1])
			if containsFold(claims[key], p.id) || strings.EqualFold(key, p.id) {
				continue
			}
			if unmapped[key] == nil {
				unmapped[key] = map[string][]string{}
			}
			unmapped[key][p.id] = append(unmapped[key][p.id], id)
		}
	}

	var issues []Issue
	for _, key := range sortedKeys(unmapped) {
		users := unmapped[key]
		owners := claims[key]
		for _, project := range sortedKeys(users) {
			n := len(users[project])
			switch {
			case len(owners) > 0:
				issues = append(issues, Issue{
					Severity: SeverityInfo, Code: IssueKeyUnmapped, Project: project, Subject: key,
					Message: fmt.Sprintf("%s here %s %s keys, which resolve to %s", plural(n, "task"), agree(n, "uses", "use"), key, owners[0]),
					Fix:     fmt.Sprintf("pass project=%s to open_task for new %s tasks that belong here", project, key),
				})
			case len(users) > 1:
				issues = append(issues, Issue{
					Severity: SeverityInfo, Code: IssueKeyUnmapped, Project: project, Subject: key,
					Message: fmt.Sprintf("%s here %s %s keys, which %d projects share, so no project claims them",
						plural(n, "task"), agree(n, "uses", "use"), key, len(users)),
					Fix: fmt.Sprintf("pass project=%s to open_task for new %s tasks that belong here", project, key),
				})
			default:
				issues = append(issues, Issue{
					Severity: SeverityWarn, Code: IssueKeyUnmapped, Project: project, Subject: key,
					Message: fmt.Sprintf("%s here %s %s keys, but no project claims %s, so open_task on a new %s key cannot find this one",
						plural(n, "task"), agree(n, "uses", "use"), key, key, key),
					Fix: fmt.Sprintf("amend_project %s add_tracker_keys=[%s]", project, key),
				})
			}
		}
	}
	return issues
}

// checkRepoNames reports a repo recorded under a bare name when a qualified
// name with the same last segment exists, platform beside acme/platform. The two
// names get separate repo notes, so the knowledge about one repository splits.
func checkRepoNames(projects []*projectData) []Issue {
	names := map[string]map[string]bool{} // repo -> projects using it
	note := func(repo, project string) {
		if repo == "" {
			return
		}
		if names[repo] == nil {
			names[repo] = map[string]bool{}
		}
		names[repo][project] = true
	}
	for _, p := range projects {
		for _, r := range p.note.Repos {
			note(r, p.id)
		}
		for _, t := range p.tasks {
			for _, r := range t.Repos {
				note(r, p.id)
			}
		}
		for _, e := range p.entries {
			note(e.Repo, p.id)
		}
	}

	var issues []Issue
	for _, bare := range sortedKeys(names) {
		if strings.Contains(bare, "/") {
			continue
		}
		var qualified []string
		for _, other := range sortedKeys(names) {
			if strings.Contains(other, "/") && strings.EqualFold(other[strings.LastIndex(other, "/")+1:], bare) {
				qualified = append(qualified, other)
			}
		}
		if len(qualified) == 0 {
			continue
		}
		for _, project := range sortedKeys(names[bare]) {
			issues = append(issues, Issue{
				Severity: SeverityWarn, Code: IssueRepoNearDuplicate, Project: project, Subject: bare,
				Message: fmt.Sprintf("repo %q looks like a short form of %s; findings under the two names are kept apart",
					bare, strings.Join(qualified, " or ")),
				Fix: fmt.Sprintf("supersede those findings with ones filed under %s", qualified[0]),
			})
		}
	}
	return issues
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func appendOnce(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

func plural(n int, word string) string {
	switch {
	case n == 1:
		return fmt.Sprintf("1 %s", word)
	case strings.HasSuffix(word, "y"):
		return fmt.Sprintf("%d %sies", n, word[:len(word)-1])
	default:
		return fmt.Sprintf("%d %ss", n, word)
	}
}

// agree picks the verb form that matches a count.
func agree(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}
