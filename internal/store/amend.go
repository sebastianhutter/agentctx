package store

import (
	"regexp"
	"sort"
	"strings"
)

// ReadProject reads a project note and folds in the amendments recorded in its
// log.
//
// A project's repos, tracker keys, environments and services are set when it
// is created and change as work finds more of them. The change is an amend
// entry in the log rather than an edit to the note, for the same reasons
// dependencies are: an agent never rewrites a shared file on a synced vault,
// every change keeps who made it, when and why, and a wrong one is undone by
// another entry. The note's
// own lists stay what they were at creation, or what a person set by hand in
// Obsidian; this is what everything else reads.
//
// Amendments apply oldest first, and within one entry additions before
// removals, so a later entry always wins. Timestamps have second resolution,
// so two amendments written in the same second touching the same repo or key
// have no defined order between them — the same limit dependencies live with.
func (s *Store) ReadProject(id string) (*ProjectNote, string, error) {
	note, body, err := s.readProjectNote(id)
	if err != nil {
		return nil, "", err
	}
	entries, err := s.Entries(id, EntryFilter{Kinds: []string{KindAmend}})
	if err != nil {
		return nil, "", err
	}
	for _, e := range entries {
		note.Repos = addFold(note.Repos, e.AddRepos...)
		note.Repos = removeFold(note.Repos, e.RemoveRepos...)
		note.JiraProjects = addFold(note.JiraProjects, e.AddKeys...)
		note.JiraProjects = removeFold(note.JiraProjects, e.RemoveKeys...)
		note.Environments = addFold(note.Environments, e.AddEnvironments...)
		note.Environments = removeFold(note.Environments, e.RemoveEnvironments...)
		note.Services = addFold(note.Services, e.AddServices...)
		note.Services = removeFold(note.Services, e.RemoveServices...)
	}
	return note, body, nil
}

// TrackerKey matches a tracker key prefix: PROJ in PROJ-142.
var TrackerKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

// taskKeyPrefix matches a task id that carries a tracker key, proj-142.
var taskKeyPrefix = regexp.MustCompile(`^([a-z][a-z0-9_]*)-\d+$`)

// KeyOfTask returns the tracker key a task id carries, upper case, or "".
func KeyOfTask(id string) string {
	m := taskKeyPrefix.FindStringSubmatch(strings.ToLower(id))
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// KeyUsage says who claims a tracker key and which tasks use it, across the
// vault. It is what decides whether claiming the key for a project is safe.
type KeyUsage struct {
	// Claimed lists projects whose tracker keys or forge groups include the
	// key, in the order resolution tries them.
	Claimed []string

	// ProjectID is set when a project's own id equals the key, which
	// resolution accepts before any claim.
	ProjectID string

	// Tasks maps a project to its task ids that use the key.
	Tasks map[string][]string
}

// KeyUsage reports how a tracker key is claimed and used.
func (s *Store) KeyUsage(key string) (*KeyUsage, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	ids, err := s.ListProjects()
	if err != nil {
		return nil, err
	}
	u := &KeyUsage{Tasks: map[string][]string{}}
	for _, id := range ids {
		if strings.EqualFold(id, key) {
			u.ProjectID = id
		}
		note, _, err := s.ReadProject(id)
		if err != nil {
			continue
		}
		for _, k := range append(append([]string{}, note.JiraProjects...), note.GitLabGroups...) {
			if strings.EqualFold(strings.TrimSpace(k), key) {
				u.Claimed = append(u.Claimed, id)
				break
			}
		}
		tasks, err := s.ListTasks(id)
		if err != nil {
			return nil, err
		}
		for _, t := range tasks {
			if KeyOfTask(t) == key {
				u.Tasks[id] = append(u.Tasks[id], t)
			}
		}
	}
	return u, nil
}

// UsedElsewhere lists the projects other than project with tasks on the key.
func (u *KeyUsage) UsedElsewhere(project string) []string {
	var out []string
	for p := range u.Tasks {
		if p != project {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func addFold(list []string, add ...string) []string {
	for _, a := range add {
		if a = strings.TrimSpace(a); a != "" && !containsFold(list, a) {
			list = append(list, a)
		}
	}
	return list
}

func removeFold(list []string, remove ...string) []string {
	if len(remove) == 0 {
		return list
	}
	out := list[:0:0]
	for _, v := range list {
		if !containsFold(remove, v) {
			out = append(out, v)
		}
	}
	return out
}
