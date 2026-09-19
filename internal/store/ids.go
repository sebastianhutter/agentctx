package store

import (
	"fmt"
	"strings"
)

// ErrInvalidID is returned for an id that could not name something inside the
// vault.
type ErrInvalidID struct {
	Kind   string
	ID     string
	Reason string
}

func (e ErrInvalidID) Error() string {
	return fmt.Sprintf("invalid %s id %q: %s", e.Kind, e.ID, e.Reason)
}

// ValidateID checks that an id names one thing inside the vault and cannot
// reach outside it.
//
// Ids become path segments. Unchecked, "../../etc" builds a path outside the
// vault entirely — which was harmless while only an agent could pass one, and
// stops being harmless the moment ids arrive from a URL. Checked here rather
// than in the web layer so every caller gets it, including the one added next
// year by someone who has not read this comment.
func ValidateID(kind, id string) error {
	switch {
	case strings.TrimSpace(id) == "":
		return ErrInvalidID{kind, id, "it is empty"}
	case id != strings.TrimSpace(id):
		return ErrInvalidID{kind, id, "it has surrounding whitespace"}
	case strings.ContainsAny(id, `/\`):
		return ErrInvalidID{kind, id, "it contains a path separator"}
	case strings.Contains(id, ".."):
		return ErrInvalidID{kind, id, "it contains .."}
	case strings.HasPrefix(id, "."):
		return ErrInvalidID{kind, id, "it starts with a dot"}
	case strings.ContainsRune(id, 0):
		return ErrInvalidID{kind, id, "it contains a null byte"}
	case len(id) > 120:
		return ErrInvalidID{kind, id, "it is longer than 120 characters"}
	}
	return nil
}

// ValidateRepo checks a repository name, which may contain one or more slashes
// because it is an org/repo pair — but is turned into a filename by sanitise,
// so it must still not be able to climb out.
func ValidateRepo(repo string) error {
	switch {
	case strings.TrimSpace(repo) == "":
		return ErrInvalidID{"repo", repo, "it is empty"}
	case strings.Contains(repo, ".."):
		return ErrInvalidID{"repo", repo, "it contains .."}
	case strings.ContainsRune(repo, 0):
		return ErrInvalidID{"repo", repo, "it contains a null byte"}
	case len(repo) > 200:
		return ErrInvalidID{"repo", repo, "it is longer than 200 characters"}
	}
	return nil
}
