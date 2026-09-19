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
