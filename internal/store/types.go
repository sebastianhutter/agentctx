// Package store is the vault's domain layer: project notes, task notes and the
// append-only log that is the source of truth for both.
//
// Note *bodies* are consolidated views, rewritten only by the consolidation
// pass. Everything an agent has to say goes into the log as a new file.
package store

import (
	"fmt"
	"strings"
	"time"
)

// Entry kinds.
const (
	KindOpened     = "opened"
	KindDecision   = "decision"
	KindFinding    = "finding"
	KindDependency = "dependency"
	KindClosed     = "closed"
	KindNote       = "note"
	KindAmend      = "amend"
)

// Finding scopes. The scope decides what survives the task, which is the whole
// point of the system: a task-scoped finding dies with the work, a repo- or
// project-scoped one is what makes the next task cheaper.
const (
	ScopeTask    = "task"
	ScopeRepo    = "repo"
	ScopeProject = "project"
)

// Source kinds, ordered by how much a claim from each is worth. See
// DeriveConfidence.
const (
	SourceFile = "file"

	// SourceMergeRequest is a proposed change under review, whatever the forge
	// calls it. GitLab says merge request, GitHub says pull request; they are
	// the same object, worth the same, mutable in the same way, and the ref
	// says which one it is. A kind per forge would only have given an agent a
	// word to hesitate over.
	SourceMergeRequest = "merge-request"

	// SourceIssue is a tracker issue — Jira, GitHub, Linear. Same reasoning.
	SourceIssue = "issue"

	SourceDocument     = "document"
	SourceConfluence   = "confluence"
	SourceWeb          = "web"
	SourceSlack        = "slack"
	SourceConversation = "conversation"
	SourceInferred     = "inferred"
)

// sourceAliases map what a caller may reasonably say to what is stored.
//
// Accepted on input, canonical on storage: an agent writes whatever its forge
// calls the thing, and the vault keeps one value so a filter over the log does
// not show the same kind twice under two names. Also keeps entries written
// before the generalisation readable.
var sourceAliases = map[string]string{
	"pull-request": SourceMergeRequest,
	"pr":           SourceMergeRequest,
	"mr":           SourceMergeRequest,
	"gitlab-mr":    SourceMergeRequest,
	"github-pr":    SourceMergeRequest,
	"jira":         SourceIssue,
	"github-issue": SourceIssue,
}

// NormaliseSourceKind resolves an alias to the kind that is stored.
func NormaliseSourceKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if canonical, ok := sourceAliases[kind]; ok {
		return canonical
	}
	return kind
}

// Confidence levels.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Timestamp layouts. Vault timestamps are local and human-readable rather than
// RFC3339: they are read in Obsidian far more often than they are parsed.
const (
	LayoutDate     = "2006-01-02"
	LayoutStamp    = "2006-01-02T15:04:05"
	LayoutMinute   = "2006-01-02T15:04"
	LayoutFilename = "2006-01-02T1504"
)

// Source records where a finding came from and when it was retrieved. Required
// on findings: a claim with no provenance is a rumour, and the staleness check
// has nothing to work with.
type Source struct {
	Kind      string `yaml:"kind"`
	Ref       string `yaml:"ref,omitempty"`
	Retrieved string `yaml:"retrieved"`
}

// Entry is one append-only log file.
type Entry struct {
	Kind       string  `yaml:"kind"`
	Task       string  `yaml:"task,omitempty"`
	Project    string  `yaml:"project"`
	Scope      string  `yaml:"scope"`
	Repo       string  `yaml:"repo,omitempty"`
	TS         string  `yaml:"ts"`
	Host       string  `yaml:"host"`
	Session    string  `yaml:"session,omitempty"`
	Source     *Source `yaml:"source,omitempty"`
	Confidence string  `yaml:"confidence,omitempty"`

	// DependsOn lists tasks this one waits for, on a dependency entry.
	DependsOn []string `yaml:"depends_on,omitempty"`

	// Consolidation marks the note entry a fold writes about itself. Excluded
	// from what a bundle reports as unconsolidated, since otherwise every fold
	// leaves its own record behind as the one thing still outstanding.
	Consolidation bool `yaml:"consolidation,omitempty"`

	// Resolved marks a dependency entry that lifts the edges it names rather
	// than declaring them. Dependencies are events, not a field: a blocker that
	// no longer blocks is a new entry, never an edit, so how the order changed
	// stays readable afterwards.
	Resolved bool `yaml:"resolved,omitempty"`

	// Supersedes names entries this one replaces, by EntryID. The log is
	// append-only, so a finding recorded wrongly — under the wrong repo name,
	// or simply wrong — cannot be edited; it is replaced. The old file stays,
	// and Entries stops returning it unless asked to.
	Supersedes []string `yaml:"supersedes,omitempty"`

	// AddRepos, RemoveRepos, AddKeys and RemoveKeys are an amend entry's
	// changes to its project's repos and tracker keys. The project note keeps
	// what was set at creation; ReadProject folds these on top. See
	// Store.ReadProject.
	AddRepos    []string `yaml:"add_repos,omitempty"`
	RemoveRepos []string `yaml:"remove_repos,omitempty"`
	AddKeys     []string `yaml:"add_tracker_keys,omitempty"`
	RemoveKeys  []string `yaml:"remove_tracker_keys,omitempty"`

	// Parent and RepoNote are links rendered from Task, Project and Repo when
	// the entry is written. See PropParent. Never read back: the ids are what
	// agentctx works from.
	Parent   string `yaml:"parent,omitempty"`
	RepoNote string `yaml:"repo_note,omitempty"`

	// Body is the entry itself and is never part of the frontmatter.
	Body string `yaml:"-"`

	// Path is where this entry was read from. Not persisted.
	Path string `yaml:"-"`

	// SupersededBy is the id of the entry that replaced this one, filled in
	// on read when EntryFilter.IncludeSuperseded asked for replaced entries.
	// Derived, never stored: the replacement is a fact about the newer file.
	SupersededBy string `yaml:"-"`
}

// ID is the entry's identifier: its file name without the extension. Unique
// within a project, since the filename carries a timestamp and a random
// suffix, and it is what Supersedes refers to.
func (e Entry) ID() string {
	return EntryID(e.Path)
}

// EntryID turns a path or file name into an entry id.
func EntryID(path string) string {
	base := path
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	return strings.TrimSuffix(base, ".md")
}

// Supersedable reports whether an entry of this kind can be replaced. Only
// what an agent asserts can be wrong in a way worth replacing: opened and
// closed record that something happened, and a dependency already has
// resolved for changing its mind.
func Supersedable(kind string) bool {
	switch kind {
	case KindFinding, KindDecision, KindNote, KindAmend:
		return true
	}
	return false
}

// Time parses the entry's timestamp. A malformed one sorts first rather than
// failing a whole bundle: a bad entry should not hide the good ones.
func (e Entry) Time() time.Time {
	t, _ := ParseTime(e.TS)
	return t
}

// TaskNote is the frontmatter of <project>/tasks/<id>.md.
//
// Written once, at creation, and thereafter only by consolidation. Deliberately
// absent: status, open MRs and blockers, which are live state that goes stale
// the moment it is stored; and last_opened/last_host, which are derived from the
// opened entries in the log rather than written back on every call.
type TaskNote struct {
	ID        string   `yaml:"id"`
	Source    string   `yaml:"source"`
	SourceKey string   `yaml:"source_key,omitempty"`
	SourceURL string   `yaml:"source_url,omitempty"`
	Title     string   `yaml:"title,omitempty"`
	Project   string   `yaml:"project"`
	Repos     []string `yaml:"repos,omitempty"`
	Created   string   `yaml:"created"`

	// ConsolidatedThrough marks how far the body has folded in the log. Entries
	// newer than this are returned separately, so consolidation is compaction
	// and never correctness.
	ConsolidatedThrough string `yaml:"consolidated_through,omitempty"`

	// Parent links the task to its project note. See PropParent.
	Parent string `yaml:"parent,omitempty"`
}

// ProjectNote is the frontmatter of <project>/<project>.md.
//
// JiraProjects and GitLabGroups are the key-prefix to project mapping: they are
// how PROJ-142 resolves to a folder. Projects are created deliberately and never
// inferred, so this mapping is always something a human wrote down.
type ProjectNote struct {
	ID                  string   `yaml:"id"`
	Name                string   `yaml:"name"`
	JiraProjects        []string `yaml:"jira_projects,omitempty"`
	GitLabGroups        []string `yaml:"gitlab_groups,omitempty"`
	Repos               []string `yaml:"repos,omitempty"`
	ConfluenceSpaces    []string `yaml:"confluence_spaces,omitempty"`
	Created             string   `yaml:"created"`
	ConsolidatedThrough string   `yaml:"consolidated_through,omitempty"`
}

// emptyTaskBody is what a new task note contains. The sections are the shape
// consolidation fills in; an agent never writes them.
const emptyTaskBody = `# %s

## Current understanding

## Decisions

## Findings

## Open threads
`

// emptyProjectBody is what a new project note contains.
const emptyProjectBody = `# %s

## What we know

## Notes

## Tasks
`

// ParseTime parses any of the layouts agentctx writes.
func ParseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{LayoutStamp, LayoutMinute, LayoutDate, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

// DeriveConfidence maps a source kind to a confidence level.
//
// Derived rather than declared because a model asked to rate its own certainty
// rates it high. What a claim is worth follows from where it came from: a file
// at a known commit can be checked, a conversation cannot, and an inference is
// not evidence at all.
func DeriveConfidence(sourceKind string) string {
	switch NormaliseSourceKind(sourceKind) {
	case SourceFile, SourceMergeRequest:
		// Pinned to a commit or a diff, so it can be re-read exactly.
		return ConfidenceHigh
	case SourceIssue, SourceDocument, SourceConfluence:
		// A real document at a named place. Re-readable, but it may have moved
		// under you — and a document is derivative: a plan or a run sheet is
		// only as good as whatever it was written from, which is why it ranks
		// below the code it describes.
		return ConfidenceMedium
	case SourceWeb:
		// A URL rots, paywalls, or changes with no version and no notice. Closer
		// to hearsay than to a document you hold.
		return ConfidenceLow
	case SourceSlack, SourceConversation:
		return ConfidenceLow
	case SourceInferred:
		return ConfidenceLow
	default:
		return ConfidenceLow
	}
}

// confidenceRank orders levels so an explicit value can be compared with the
// derived one.
func confidenceRank(level string) int {
	switch level {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

// ResolveConfidence combines the derived confidence with what the caller asked
// for. A caller may lower it but never raise it: doubt is worth recording,
// certainty has to be earned by provenance.
func ResolveConfidence(sourceKind, requested string) string {
	derived := DeriveConfidence(sourceKind)
	if requested == "" {
		return derived
	}
	if confidenceRank(requested) > 0 && confidenceRank(requested) < confidenceRank(derived) {
		return requested
	}
	return derived
}

// movingRefs are the names a ref must not pin to. Each of them points at
// something different tomorrow.
var movingRefs = map[string]bool{
	"head": true, "main": true, "master": true, "trunk": true,
	"develop": true, "dev": true, "latest": true,
}

// ValidateFileRef checks that a file reference names a fixed point in history.
//
// The whole purpose of a ref is that a finding can be re-checked against what
// it was taken from. `modules/vpc/main.tf@abc1234` can be; `…@origin/main`
// cannot — by the time anyone looks, main has moved, and the finding is
// unverifiable in a way nothing reports. Seen in real use within the first
// session, which is why this is a check and not a sentence in a description.
func ValidateFileRef(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil // optional; the finding is simply weaker without one
	}

	at := strings.LastIndex(ref, "@")
	if at < 0 {
		return fmt.Errorf("a file ref should pin a commit, as path@commit — %q names no revision, "+
			"so the finding cannot be re-checked later", ref)
	}

	rev := strings.ToLower(strings.TrimSpace(ref[at+1:]))
	if rev == "" {
		return fmt.Errorf("a file ref should pin a commit, as path@commit — %q ends at the @", ref)
	}
	if i := strings.LastIndex(rev, "/"); i >= 0 {
		// origin/main, refs/heads/main — a remote-tracking name is still a name.
		rev = rev[i+1:]
	}
	if movingRefs[rev] {
		return fmt.Errorf("%q pins a branch, which moves: use the commit it is at today "+
			"(git rev-parse --short HEAD), so the finding can be re-checked against what you read", ref)
	}
	return nil
}

// ValidSourceKind reports whether kind is one agentctx knows how to weigh.
func ValidSourceKind(kind string) bool {
	switch NormaliseSourceKind(kind) {
	case SourceFile, SourceMergeRequest, SourceIssue, SourceDocument, SourceConfluence,
		SourceWeb, SourceSlack, SourceConversation, SourceInferred:
		return true
	}
	return false
}

// SourceKinds lists the valid source kinds, for error messages and tool schemas.
//
// Ordered strongest first, because the list is read by a model choosing one and
// the order is itself the advice.
func SourceKinds() []string {
	return []string{SourceFile, SourceMergeRequest, SourceIssue, SourceDocument,
		SourceConfluence, SourceWeb, SourceSlack, SourceConversation, SourceInferred}
}
