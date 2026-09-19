package store

import (
	"strings"
	"testing"
)

// Ids become path segments. Unchecked, they reach outside the vault — harmless
// while only an agent could pass one, a file-read hole once they arrive from a
// URL.
func TestValidateID(t *testing.T) {
	for _, tc := range []struct{ id, wantReason string }{
		{"proj", ""},
		{"proj-142", ""},
		{"rq_transporter.v2", ""},
		{"", "empty"},
		{"  ", "empty"},
		{" proj", "whitespace"},
		{"../../etc", "path separator"},
		{"..", ".."},
		{"a..b", ".."},
		{"a/b", "path separator"},
		{`a\b`, "path separator"},
		{".hidden", "starts with a dot"},
		{"a\x00b", "null byte"},
		{strings.Repeat("a", 121), "longer than"},
	} {
		err := ValidateID("task", tc.id)
		switch {
		case tc.wantReason == "" && err != nil:
			t.Errorf("ValidateID(%q) = %v, want accepted", tc.id, err)
		case tc.wantReason != "" && err == nil:
			t.Errorf("ValidateID(%q) accepted, want rejected for %q", tc.id, tc.wantReason)
		case tc.wantReason != "" && err != nil && !strings.Contains(err.Error(), tc.wantReason):
			t.Errorf("ValidateID(%q) = %v, want it to mention %q", tc.id, err, tc.wantReason)
		}
	}
}

func TestValidateRepo(t *testing.T) {
	// A repo is an org/repo pair, so slashes are legitimate — but it still
	// becomes a filename, so it still must not climb.
	if err := ValidateRepo("acme/infrastructure"); err != nil {
		t.Errorf("a normal repo name was rejected: %v", err)
	}
	if err := ValidateRepo("../../etc/passwd"); err == nil {
		t.Error("a climbing repo name was accepted")
	}
	if err := ValidateRepo(""); err == nil {
		t.Error("an empty repo name was accepted")
	}
}

// The check has to bite at the API, not only in the helper.
func TestStoreRejectsTraversal(t *testing.T) {
	s := New(t.TempDir())
	if err := s.CreateProject(&ProjectNote{ID: "proj", Name: "Example"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if _, _, err := s.ReadProject("../../etc"); err == nil {
		t.Error("ReadProject accepted a climbing id")
	}
	if _, _, err := s.ReadTask("proj", "../../../secrets"); err == nil {
		t.Error("ReadTask accepted a climbing id")
	}
	if s.TaskExists("proj", "../../../secrets") {
		t.Error("TaskExists accepted a climbing id")
	}
	if _, err := s.Entries("../../etc", EntryFilter{}); err == nil {
		t.Error("Entries accepted a climbing id")
	}
	if _, err := s.ListTasks("../.."); err == nil {
		t.Error("ListTasks accepted a climbing id")
	}
	if _, err := s.Append(&Entry{Kind: KindNote, Project: "../../etc", Body: "x"}); err == nil {
		t.Error("Append accepted a climbing project")
	}
	if _, err := s.Append(&Entry{Kind: KindNote, Project: "proj", Task: "../../x", Body: "x"}); err == nil {
		t.Error("Append accepted a climbing task")
	}
}
