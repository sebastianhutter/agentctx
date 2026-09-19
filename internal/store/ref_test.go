package store

import (
	"strings"
	"testing"
)

// Seen in the first real project: a finding pinned to @origin/main, which is
// unverifiable the moment main moves, and nothing reported it.
func TestValidateFileRef(t *testing.T) {
	for _, tc := range []struct {
		ref     string
		wantErr string
	}{
		{"modules/vpc/main.tf@abc1234", ""},
		{"acme/infrastructure@c6f88bf", ""},
		{"", ""}, // optional
		{"modules/vpc/main.tf@origin/main", "pins a branch"},
		{"acme/infra modules/zitadel@origin/main", "pins a branch"},
		{"something@HEAD", "pins a branch"},
		{"something@develop", "pins a branch"},
		{"modules/vpc/main.tf", "names no revision"},
		{"modules/vpc/main.tf@", "ends at the @"},
	} {
		err := ValidateFileRef(tc.ref)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("ValidateFileRef(%q) = %v, want accepted", tc.ref, err)
		case tc.wantErr != "" && err == nil:
			t.Errorf("ValidateFileRef(%q) was accepted, want %q", tc.ref, tc.wantErr)
		case tc.wantErr != "" && err != nil && !strings.Contains(err.Error(), tc.wantErr):
			t.Errorf("ValidateFileRef(%q) = %v, want it to mention %q", tc.ref, err, tc.wantErr)
		}
	}
}

// The taxonomy earns a kind only where it changes behaviour: what the claim is
// worth, whether it will have moved, and which tool re-checks it. Two kinds
// that answer all three the same way are one kind.
func TestDerivedConfidenceRanksSources(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		// Pinned: re-readable exactly.
		{SourceFile, ConfidenceHigh},
		{SourceMergeRequest, ConfidenceHigh},
		// A real document at a named place, but derivative or mutable.
		{SourceIssue, ConfidenceMedium},
		{SourceDocument, ConfidenceMedium},
		{SourceConfluence, ConfidenceMedium},
		// A URL rots and changes with no version — nearer hearsay than a document.
		{SourceWeb, ConfidenceLow},
		{SourceSlack, ConfidenceLow},
		{SourceConversation, ConfidenceLow},
		{SourceInferred, ConfidenceLow},
	} {
		if got := DeriveConfidence(tc.kind); got != tc.want {
			t.Errorf("DeriveConfidence(%q) = %q, want %q", tc.kind, got, tc.want)
		}
	}

	// A document must not outrank the code it describes: a plan's tables are
	// illustrative, the source is authoritative.
	if confidenceRank(DeriveConfidence(SourceDocument)) >= confidenceRank(DeriveConfidence(SourceFile)) {
		t.Error("a document ranks at or above the code it was written from")
	}
}

func TestEverySourceKindIsListedAndValid(t *testing.T) {
	for _, kind := range SourceKinds() {
		if !ValidSourceKind(kind) {
			t.Errorf("SourceKinds lists %q but ValidSourceKind rejects it", kind)
		}
	}
	// The listed order is advice to whoever is choosing, so it must run
	// strongest to weakest.
	last := 4
	for _, kind := range SourceKinds() {
		rank := confidenceRank(DeriveConfidence(kind))
		if rank > last {
			t.Errorf("SourceKinds is not ordered strongest first: %q outranks what precedes it", kind)
		}
		last = rank
	}
}

// The forge is not a property of the claim: a GitLab merge request and a GitHub
// pull request are the same object, and the ref says which. Both words are
// accepted so an agent never has to hesitate over vocabulary, and one value is
// stored so a filter over the log does not show the same kind twice.
func TestSourceAliasesNormalise(t *testing.T) {
	for _, tc := range []struct{ given, want string }{
		{"pull-request", SourceMergeRequest},
		{"PULL-REQUEST", SourceMergeRequest},
		{"gitlab-mr", SourceMergeRequest},
		{"github-pr", SourceMergeRequest},
		{"pr", SourceMergeRequest},
		{"mr", SourceMergeRequest},
		{"jira", SourceIssue},
		{"github-issue", SourceIssue},
		{"merge-request", SourceMergeRequest},
		{"file", SourceFile},
		{"nonsense", "nonsense"},
	} {
		if got := NormaliseSourceKind(tc.given); got != tc.want {
			t.Errorf("NormaliseSourceKind(%q) = %q, want %q", tc.given, got, tc.want)
		}
	}

	// Entries written before the generalisation must stay readable and keep
	// their weight.
	for _, legacy := range []string{"gitlab-mr", "jira"} {
		if !ValidSourceKind(legacy) {
			t.Errorf("the legacy kind %q is no longer valid", legacy)
		}
	}
	if DeriveConfidence("gitlab-mr") != ConfidenceHigh {
		t.Error("a legacy gitlab-mr finding lost its confidence")
	}
	if DeriveConfidence("jira") != ConfidenceMedium {
		t.Error("a legacy jira finding lost its confidence")
	}

	// Aliases are not offered: the list a model chooses from stays one word per
	// thing.
	for _, kind := range SourceKinds() {
		if _, isAlias := sourceAliases[kind]; isAlias {
			t.Errorf("SourceKinds offers %q, which is an alias", kind)
		}
	}
}
