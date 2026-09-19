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
		{SourceGitLabMR, ConfidenceHigh},
		// A real document at a named place, but derivative or mutable.
		{SourceJira, ConfidenceMedium},
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
