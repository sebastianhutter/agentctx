package service

import (
	"errors"
	"fmt"
	"strings"

	"github.com/sebastianhutter/agentctx/internal/store"
)

// --- amend_project ---

// AmendProjectRequest adds or removes repositories, tracker keys,
// environments and services on an existing project.
type AmendProjectRequest struct {
	ProjectID   string
	AddRepos    []string
	RemoveRepos []string
	AddKeys     []string
	RemoveKeys  []string

	AddEnvironments    []string
	RemoveEnvironments []string
	AddServices        []string
	RemoveServices     []string

	Reason string
}

// AmendProject records a change to a project's repos or tracker keys.
//
// A project is created deliberately, but what belongs to it is found out as
// the work goes: a repository turns up mid-task, the tracker a project's tasks
// come from was never written down. Without this, the vault drifts until a
// person edits the note by hand — and in real use, agents wrote a workaround
// into a project note ("always pass project") rather than leave the gap.
//
// Repos are cheap to get wrong: a wrong one only adds findings to a bundle.
// A tracker key is not, since it decides which project every future task with
// that key is filed under. So a key is refused when claiming it here would
// take it from, or share it with, another project — the shared-tracker case,
// where passing project explicitly is the right answer — and when no task in
// this project uses it yet, which catches a mistyped or speculative key. Every
// amendment is an entry, so a wrong one is undone by another.
func (s *Service) AmendProject(req AmendProjectRequest) (*store.Entry, error) {
	id := strings.TrimSpace(req.ProjectID)
	note, _, err := s.Store.ReadProject(id)
	if err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return nil, errors.New("reason is required: an amendment nobody can explain cannot be judged when it turns out wrong")
	}

	entry := &store.Entry{
		Kind: store.KindAmend, Project: id, Scope: store.ScopeProject,
		Host: s.host(), Session: s.session(),
	}

	known, err := s.reposInUse(id)
	if err != nil {
		return nil, err
	}
	for _, repo := range clean(req.AddRepos) {
		if err := store.ValidateRepo(repo); err != nil {
			return nil, err
		}
		if containsFold(note.Repos, repo) {
			continue
		}
		if q := qualifiedFormOf(repo, append(append([]string{}, note.Repos...), known...)); q != "" {
			return nil, fmt.Errorf("repo %q looks like a short form of %s, which this project already uses: "+
				"add the qualified name, so findings under the two are not kept apart", repo, q)
		}
		entry.AddRepos = append(entry.AddRepos, repo)
	}
	for _, repo := range clean(req.RemoveRepos) {
		if !containsFold(note.Repos, repo) {
			return nil, fmt.Errorf("repo %q is not one of project %s's repos (%s)", repo, id, strings.Join(note.Repos, ", "))
		}
		entry.RemoveRepos = append(entry.RemoveRepos, repo)
	}

	for _, key := range clean(req.AddKeys) {
		key = strings.ToUpper(key)
		if !store.TrackerKey.MatchString(key) {
			return nil, fmt.Errorf("tracker key %q should be the prefix alone, as PROJ for PROJ-142", key)
		}
		if containsFold(note.JiraProjects, key) {
			continue
		}
		if err := s.checkKeyClaim(id, key); err != nil {
			return nil, err
		}
		entry.AddKeys = append(entry.AddKeys, key)
	}
	for _, key := range clean(req.RemoveKeys) {
		key = strings.ToUpper(key)
		if !containsFold(note.JiraProjects, key) {
			return nil, fmt.Errorf("tracker key %q is not claimed by project %s", key, id)
		}
		entry.RemoveKeys = append(entry.RemoveKeys, key)
	}

	// Environments and services cost what a repo costs to get wrong: a wrong
	// one adds findings to a bundle, and another amendment takes it away.
	for _, sub := range []struct {
		kind           string
		have           []string
		add, remove    []string
		added, removed *[]string
	}{
		{store.ScopeEnvironment, note.Environments, req.AddEnvironments, req.RemoveEnvironments,
			&entry.AddEnvironments, &entry.RemoveEnvironments},
		{store.ScopeService, note.Services, req.AddServices, req.RemoveServices,
			&entry.AddServices, &entry.RemoveServices},
	} {
		for _, v := range clean(sub.add) {
			if err := store.ValidateSubject(sub.kind, v); err != nil {
				return nil, err
			}
			if !containsFold(sub.have, v) {
				*sub.added = append(*sub.added, v)
			}
		}
		for _, v := range clean(sub.remove) {
			if !containsFold(sub.have, v) {
				return nil, fmt.Errorf("%s %q is not one of project %s's (%s)", sub.kind, v, id, strings.Join(sub.have, ", "))
			}
			*sub.removed = append(*sub.removed, v)
		}
	}

	if len(entry.AddRepos)+len(entry.RemoveRepos)+len(entry.AddKeys)+len(entry.RemoveKeys)+
		len(entry.AddEnvironments)+len(entry.RemoveEnvironments)+len(entry.AddServices)+len(entry.RemoveServices) == 0 {
		return nil, fmt.Errorf("nothing to change: project %s already has what was asked", id)
	}

	var body strings.Builder
	body.WriteString(reason)
	body.WriteString("\n")
	for _, line := range []struct {
		label string
		items []string
	}{
		{"Adds repos", entry.AddRepos}, {"Removes repos", entry.RemoveRepos},
		{"Claims tracker keys", entry.AddKeys}, {"Releases tracker keys", entry.RemoveKeys},
		{"Adds environments", entry.AddEnvironments}, {"Removes environments", entry.RemoveEnvironments},
		{"Adds services", entry.AddServices}, {"Removes services", entry.RemoveServices},
	} {
		if len(line.items) > 0 {
			fmt.Fprintf(&body, "\n**%s:** %s", line.label, strings.Join(line.items, ", "))
		}
	}
	entry.Body = body.String()

	if _, err := s.Store.Append(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// checkKeyClaim refuses a key claim that would route tasks wrongly.
func (s *Service) checkKeyClaim(project, key string) error {
	u, err := s.Store.KeyUsage(key)
	if err != nil {
		return err
	}
	switch {
	case u.ProjectID != "" && u.ProjectID != project:
		return fmt.Errorf("%s already resolves to project %s by its id, so a claim here would never be used — "+
			"pass project=%s to open_task instead", key, u.ProjectID, project)
	case len(u.Claimed) > 0 && !(len(u.Claimed) == 1 && u.Claimed[0] == project):
		return fmt.Errorf("%s is claimed by %s: a new %s key would go there, and claiming it here too would make "+
			"that depend on directory order — pass project=%s to open_task for tasks that belong here",
			key, strings.Join(u.Claimed, ", "), key, project)
	}
	if other := u.UsedElsewhere(project); len(other) > 0 {
		return fmt.Errorf("%s is also used by tasks in %s: it is a tracker shared between projects, and claiming "+
			"it here would file all of their future tasks under this one — leave it unclaimed and pass project=%s "+
			"to open_task", key, strings.Join(other, ", "), project)
	}
	if len(u.Tasks[project]) == 0 {
		return fmt.Errorf("no task in project %s uses %s yet: open the first one with project=%s, then claim the key "+
			"(this is what catches a mistyped key before it takes effect)", project, key, project)
	}
	return nil
}

// reposInUse lists the repos a project's tasks and entries name, whether or
// not the project lists them.
func (s *Service) reposInUse(project string) ([]string, error) {
	var repos []string
	tasks, err := s.Store.ListTasks(project)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if tn, _, err := s.Store.ReadTask(project, t); err == nil {
			for _, r := range tn.Repos {
				repos = appendFoldOnce(repos, r)
			}
		}
	}
	entries, err := s.Store.Entries(project, store.EntryFilter{})
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Repo != "" {
			repos = appendFoldOnce(repos, e.Repo)
		}
	}
	return repos, nil
}

// qualifiedFormOf returns a repo in candidates that is the qualified form of
// a bare name, platform for acme/platform, or "".
func qualifiedFormOf(repo string, candidates []string) string {
	if strings.Contains(repo, "/") {
		return ""
	}
	for _, c := range candidates {
		if i := strings.LastIndex(c, "/"); i >= 0 && strings.EqualFold(c[i+1:], repo) {
			return c
		}
	}
	return ""
}

// ProjectGap is something a task shows its project is missing, with the call
// that closes it.
type ProjectGap struct {
	Message string
	Fix     string
}

// ProjectGaps reports what a task shows its project is missing: a repo the
// task works in that the project does not list, and a tracker key the task
// uses that nothing claims and nothing else uses.
//
// Surfaced in open_task, because the agent holding the task is the one who
// knows whether the repo or key belongs, and a checks page nobody opens fixes
// nothing. A key is only suggested when claiming it would pass checkKeyClaim;
// a shared or claimed key is not a gap, it is how that project works.
func (s *Service) ProjectGaps(project, taskID string) []ProjectGap {
	note, _, err := s.Store.ReadProject(project)
	if err != nil {
		return nil
	}
	task, _, err := s.Store.ReadTask(project, taskID)
	if err != nil {
		return nil
	}

	var gaps []ProjectGap
	var missing []string
	for _, r := range task.Repos {
		if !containsFold(note.Repos, r) && qualifiedFormOf(r, note.Repos) == "" {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		gaps = append(gaps, ProjectGap{
			Message: fmt.Sprintf("This task works in %s, which project %s does not list, so findings about it "+
				"are not shown to the next task in the project.", strings.Join(missing, ", "), project),
			Fix: fmt.Sprintf("amend_project(project_id=%q, add_repos=[%s], reason=…) if it belongs to the project",
				project, quoteList(missing)),
		})
	}

	// A key equal to the project's own id already resolves here.
	if key := s.taskKey(task); key != "" && !strings.EqualFold(key, project) &&
		!containsFold(note.JiraProjects, key) && !containsFold(note.GitLabGroups, key) {
		if s.checkKeyClaim(project, key) == nil {
			gaps = append(gaps, ProjectGap{
				Message: fmt.Sprintf("No project claims tracker key %s, so open_task on the next %s key finds no project "+
					"unless it is told this one.", key, key),
				Fix: fmt.Sprintf("amend_project(project_id=%q, add_tracker_keys=[%q], reason=…) if %s's tasks belong here",
					project, key, key),
			})
		}
	}
	return gaps
}

// taskKey returns the tracker key a task was filed under, if it came from a
// tracker at all.
func (s *Service) taskKey(t *store.TaskNote) string {
	if t.SourceKey != "" {
		if i := strings.LastIndex(t.SourceKey, "-"); i > 0 {
			if k := strings.ToUpper(t.SourceKey[:i]); store.TrackerKey.MatchString(k) {
				return k
			}
		}
	}
	if t.Source == "jira" || t.Source == "gitlab" || t.Source == "github" {
		return store.KeyOfTask(t.ID)
	}
	return ""
}

func clean(list []string) []string {
	var out []string
	for _, v := range list {
		if v = strings.TrimSpace(v); v != "" {
			out = appendFoldOnce(out, v)
		}
	}
	return out
}

func containsFold(list []string, v string) bool {
	for _, have := range list {
		if strings.EqualFold(strings.TrimSpace(have), v) {
			return true
		}
	}
	return false
}

func appendFoldOnce(list []string, v string) []string {
	if containsFold(list, v) {
		return list
	}
	return append(list, v)
}

func quoteList(items []string) string {
	quoted := make([]string, len(items))
	for i, v := range items {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}
