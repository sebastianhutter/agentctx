package store

import (
	"fmt"
	"strings"
)

// Subject scopes beyond repo. A finding scoped to one of these is about a thing
// that outlives the task, as a repository does, and is found again by the next
// piece of work that touches the same thing.
const (
	// ScopeEnvironment is a deployment environment: an account, a cluster, a
	// stage. What is recorded is what was learned about it, never an inventory
	// of it — live state goes stale the moment it is stored.
	ScopeEnvironment = "environment"

	// ScopeService is a service the work depends on: a SaaS product, or a
	// self-hosted instance of one.
	ScopeService = "service"
)

// Subject names one thing findings accumulate about: a repository, an
// environment or a service.
//
// Every subject id names one thing wherever it is logged, which is what lets
// the bundles read a subject's findings across projects. For a repository that
// comes free with the org/repo name. For an environment it is enforced: every
// organisation has a "production", so the id carries its owner.
type Subject struct {
	Kind string // ScopeRepo, ScopeEnvironment or ScopeService
	ID   string
}

// String renders a subject as its kind and id, "environment acme/production".
func (s Subject) String() string { return s.Kind + " " + s.ID }

// SubjectKinds lists the scopes that name a subject, in the order bundles show
// them.
func SubjectKinds() []string {
	return []string{ScopeRepo, ScopeEnvironment, ScopeService}
}

// IsSubjectScope reports whether a scope names a subject rather than a task or
// a project.
func IsSubjectScope(scope string) bool {
	switch scope {
	case ScopeRepo, ScopeEnvironment, ScopeService:
		return true
	}
	return false
}

// Subject returns what a subject-scoped entry is about. False for an entry
// scoped to a task or a project, and for one whose scope names a subject it
// does not carry.
func (e Entry) Subject() (Subject, bool) {
	var id string
	switch e.Scope {
	case ScopeRepo:
		id = e.Repo
	case ScopeEnvironment:
		id = e.Environment
	case ScopeService:
		id = e.Service
	default:
		return Subject{}, false
	}
	if id == "" {
		return Subject{}, false
	}
	return Subject{Kind: e.Scope, ID: id}, true
}

// SubjectsOf returns a project's listed subjects of one kind.
func (n *ProjectNote) SubjectsOf(kind string) []string {
	switch kind {
	case ScopeRepo:
		return n.Repos
	case ScopeEnvironment:
		return n.Environments
	case ScopeService:
		return n.Services
	}
	return nil
}

// ValidateSubject checks a subject id for its kind.
//
// All three become file names through sanitise, so none may climb out of the
// vault. An environment must also say whose it is: acme/production, not
// production, since a bare stage name would join every project's production
// into one.
func ValidateSubject(kind, id string) error {
	switch kind {
	case ScopeRepo:
		return ValidateRepo(id)
	case ScopeEnvironment, ScopeService:
	default:
		return fmt.Errorf("%q is not a subject kind (one of %s)", kind, strings.Join(SubjectKinds(), ", "))
	}

	switch {
	case strings.TrimSpace(id) == "":
		return ErrInvalidID{kind, id, "it is empty"}
	case id != strings.TrimSpace(id):
		return ErrInvalidID{kind, id, "it has surrounding whitespace"}
	case strings.Contains(id, ".."):
		return ErrInvalidID{kind, id, "it contains .."}
	case strings.ContainsRune(id, 0):
		return ErrInvalidID{kind, id, "it contains a null byte"}
	case len(id) > 200:
		return ErrInvalidID{kind, id, "it is longer than 200 characters"}
	}

	if kind == ScopeEnvironment {
		owner, name, ok := strings.Cut(id, "/")
		if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
			return ErrInvalidID{kind, id, "it needs its owner, as acme/production: every organisation has a " +
				"production, and a bare name would join them all into one"}
		}
	}
	return nil
}

// ProjectSubjects returns the subjects of one kind a project works with: the
// ones its note lists, then any its own log has findings about without listing
// them.
//
// Both, because either alone loses something. The list is what brings in other
// projects' knowledge of a subject before this project has logged a word about
// it; the log is what keeps a finding logged under an unlisted subject from
// vanishing out of every bundle until someone amends the project.
func (s *Store) ProjectSubjects(project, kind string) ([]string, error) {
	note, _, err := s.ReadProject(project)
	if err != nil {
		return nil, err
	}
	ids := append([]string{}, note.SubjectsOf(kind)...)
	entries, err := s.Entries(project, EntryFilter{Scope: kind, Kinds: []string{KindFinding}})
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if sub, ok := e.Subject(); ok && !containsFold(ids, sub.ID) {
			ids = append(ids, sub.ID)
		}
	}
	return ids, nil
}
