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
	KindOpened   = "opened"
	KindDecision = "decision"
	KindFinding  = "finding"
	KindClosed   = "closed"
	KindNote     = "note"
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
	SourceFile         = "file"
	SourceGitLabMR     = "gitlab-mr"
	SourceJira         = "jira"
	SourceConfluence   = "confluence"
	SourceSlack        = "slack"
	SourceConversation = "conversation"
	SourceInferred     = "inferred"
)

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

	// Body is the entry itself and is never part of the frontmatter.
	Body string `yaml:"-"`

	// Path is where this entry was read from. Not persisted.
	Path string `yaml:"-"`
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
	switch sourceKind {
	case SourceFile, SourceGitLabMR:
		return ConfidenceHigh
	case SourceJira, SourceConfluence:
		return ConfidenceMedium
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

// ValidSourceKind reports whether kind is one agentctx knows how to weigh.
func ValidSourceKind(kind string) bool {
	switch kind {
	case SourceFile, SourceGitLabMR, SourceJira, SourceConfluence,
		SourceSlack, SourceConversation, SourceInferred:
		return true
	}
	return false
}

// SourceKinds lists the valid source kinds, for error messages and tool schemas.
func SourceKinds() []string {
	return []string{SourceFile, SourceGitLabMR, SourceJira, SourceConfluence,
		SourceSlack, SourceConversation, SourceInferred}
}
