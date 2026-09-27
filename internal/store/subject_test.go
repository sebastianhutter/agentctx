package store

import (
	"strings"
	"testing"
)

func TestValidateSubject(t *testing.T) {
	for _, c := range []struct {
		kind, id string
		ok       bool
	}{
		{ScopeEnvironment, "acme/production", true},
		{ScopeEnvironment, "production", false},
		{ScopeEnvironment, "/production", false},
		{ScopeEnvironment, "acme/", false},
		{ScopeService, "idp", true},
		{ScopeService, "acme/idp", true},
		{ScopeService, "../../etc", false},
		{ScopeService, " idp", false},
		{ScopeRepo, "acme/platform", true},
		{"cluster", "acme/one", false},
	} {
		if err := ValidateSubject(c.kind, c.id); (err == nil) != c.ok {
			t.Errorf("ValidateSubject(%q, %q) = %v, want ok=%v", c.kind, c.id, err, c.ok)
		}
	}
}

func TestSubjectPagesDoNotCollide(t *testing.T) {
	s := New(t.TempDir())
	repo := s.SubjectNotePath("proj", Subject{ScopeRepo, "acme/production"})
	env := s.SubjectNotePath("proj", Subject{ScopeEnvironment, "acme/production"})
	svc := s.SubjectNotePath("proj", Subject{ScopeService, "acme/production"})
	if repo == env || env == svc || repo == svc {
		t.Errorf("one id of three kinds shares a page: %s, %s, %s", repo, env, svc)
	}
	if !strings.HasSuffix(env, "notes/environments/acme-production.md") {
		t.Errorf("environment page at %s", env)
	}
}

func TestAppendRefusesAMismatchedSubject(t *testing.T) {
	s := New(t.TempDir())
	if err := s.CreateProject(&ProjectNote{ID: "proj", Name: "Example"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, e := range []*Entry{
		{Kind: KindFinding, Project: "proj", Scope: ScopeTask, Environment: "acme/production"},
		{Kind: KindFinding, Project: "proj", Scope: ScopeService},
	} {
		if _, err := s.Append(e); err == nil {
			t.Errorf("Append(%+v) was accepted", e)
		}
	}
}
