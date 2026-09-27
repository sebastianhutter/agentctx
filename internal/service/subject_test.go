package service

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastianhutter/agentctx/internal/store"
)

func logSubject(t *testing.T, s *Service, target, scope, env, svc, text string) *store.Entry {
	t.Helper()
	e, err := s.LogFinding(LogFindingRequest{
		TargetID: target, Finding: text, Scope: scope, Environment: env, Service: svc,
		Source: store.Source{Kind: store.SourceDocument, Ref: "runbook"},
	})
	if err != nil {
		t.Fatalf("LogFinding %s: %v", text, err)
	}
	return e
}

func TestLogFindingAboutEnvironmentsAndServices(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	env := logSubject(t, s, "proj", store.ScopeEnvironment, "acme/production", "", "public buckets are denied by policy")
	if env.Environment != "acme/production" || env.Scope != store.ScopeEnvironment {
		t.Errorf("entry = %+v, want an environment-scoped finding", env)
	}
	svc := logSubject(t, s, "proj", store.ScopeService, "", "idp", "introspection is rate-limited")
	if sub, ok := svc.Subject(); !ok || sub != (store.Subject{Kind: store.ScopeService, ID: "idp"}) {
		t.Errorf("Subject() = %v, %v", sub, ok)
	}

	refusals := []struct {
		name string
		req  LogFindingRequest
		want string
	}{
		{"environment without its scope",
			LogFindingRequest{Environment: "acme/production"}, "pass scope=environment"},
		{"scope without its environment",
			LogFindingRequest{Scope: store.ScopeEnvironment}, "environment is required"},
		{"a bare environment name",
			LogFindingRequest{Scope: store.ScopeEnvironment, Environment: "production"}, "needs its owner"},
		{"a service on a repo finding",
			LogFindingRequest{Scope: store.ScopeRepo, Repo: "acme/infrastructure", Service: "idp"}, "pass scope=service"},
		{"an id that climbs out",
			LogFindingRequest{Scope: store.ScopeService, Service: "../../etc"}, ".."},
		{"an unknown scope",
			LogFindingRequest{Scope: "cluster"}, "not one of"},
	}
	for _, r := range refusals {
		r.req.TargetID, r.req.Finding = "proj", "x"
		r.req.Source = store.Source{Kind: store.SourceDocument}
		_, err := s.LogFinding(r.req)
		if err == nil || !strings.Contains(err.Error(), r.want) {
			t.Errorf("%s: err = %v, want it to say %q", r.name, err, r.want)
		}
	}
}

func TestBundlesShowEnvironmentsAndServices(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	newProject(t, s, "other")

	// Listed on one project and logged about by another: listing is what brings
	// the other project's knowledge in before anything is logged here.
	if err := amend(s, AmendProjectRequest{ProjectID: "proj",
		AddEnvironments: []string{"acme/production"}, AddServices: []string{"idp"}}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	logSubject(t, s, "other", store.ScopeEnvironment, "acme/production", "", "public buckets are denied by policy")
	logSubject(t, s, "other", store.ScopeService, "", "Idp", "introspection is rate-limited")
	logSubject(t, s, "other", store.ScopeEnvironment, "acme/staging", "", "unrelated environment fact")

	// Logged here but not listed: must not vanish from the bundle.
	openIn(t, s, "proj", "proj-1")
	logSubject(t, s, "proj-1", store.ScopeService, "", "acme/vault", "secrets engine is kv v2")

	b, err := s.OpenTask(OpenTaskRequest{TaskID: "proj-2", Project: "proj", Seed: &Seed{Title: "next"}})
	if err != nil {
		t.Fatalf("OpenTask: %v", err)
	}
	out := b.Render()
	for _, want := range []string{
		"## What we know about service acme/vault", "secrets engine is kv v2",
		"## What other projects know about environment acme/production", "public buckets are denied by policy",
		"## What other projects know about service idp", "introspection is rate-limited",
		"from project other",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("task bundle is missing %q", want)
		}
	}
	if strings.Contains(out, "unrelated environment fact") {
		t.Error("task bundle shows an environment the project neither lists nor mentions")
	}

	pb, err := s.OpenProject(OpenProjectRequest{ID: "proj"})
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	pout := pb.Render()
	for _, want := range []string{"- environments: acme/production", "- services: idp",
		"What other projects know about environment acme/production"} {
		if !strings.Contains(pout, want) {
			t.Errorf("project bundle is missing %q", want)
		}
	}
}

func TestElsewhereFindingsAreHeadlines(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	newProject(t, s, "other", "acme/infrastructure")

	long := "The state bucket is shared by every stack " + strings.Repeat("and more detail ", 30) + "END"
	if _, err := s.LogFinding(LogFindingRequest{TargetID: "other", Scope: store.ScopeRepo, Repo: "acme/infrastructure",
		Finding: long + "\n\nA second paragraph nobody reads here.",
		Source:  store.Source{Kind: store.SourceDocument}}); err != nil {
		t.Fatalf("LogFinding: %v", err)
	}
	pb, err := s.OpenProject(OpenProjectRequest{ID: "proj"})
	if err != nil {
		t.Fatalf("OpenProject: %v", err)
	}
	out := pb.Render()
	if !strings.Contains(out, "The state bucket is shared by every stack") || !strings.Contains(out, " …") {
		t.Error("a finding from elsewhere should show as a shortened headline")
	}
	for _, leak := range []string{"END", "second paragraph"} {
		if strings.Contains(out, leak) {
			t.Errorf("headline leaked %q from the full finding", leak)
		}
	}
}

func TestAmendProjectEnvironmentsAndServices(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)

	if err := amend(s, AmendProjectRequest{ProjectID: "proj", AddEnvironments: []string{"production"}}); err == nil ||
		!strings.Contains(err.Error(), "needs its owner") {
		t.Errorf("a bare environment was accepted: %v", err)
	}
	if err := amend(s, AmendProjectRequest{ProjectID: "proj",
		AddEnvironments: []string{"acme/staging", "acme/production"}, AddServices: []string{"idp"}}); err != nil {
		t.Fatalf("amend: %v", err)
	}
	// Amendments in one second have no defined order, so the removal comes later.
	s.SetClock(func() time.Time { return now.Add(time.Minute) })
	if err := amend(s, AmendProjectRequest{ProjectID: "proj", RemoveEnvironments: []string{"acme/staging"}}); err != nil {
		t.Fatalf("amend remove: %v", err)
	}
	if err := amend(s, AmendProjectRequest{ProjectID: "proj", RemoveServices: []string{"sentry"}}); err == nil {
		t.Error("removing a service the project does not have was accepted")
	}
	if err := amend(s, AmendProjectRequest{ProjectID: "proj", AddServices: []string{"Idp"}}); err == nil ||
		!strings.Contains(err.Error(), "nothing to change") {
		t.Errorf("re-adding a service in another case should change nothing: %v", err)
	}

	note, _, err := s.Store.ReadProject("proj")
	if err != nil {
		t.Fatalf("ReadProject: %v", err)
	}
	if strings.Join(note.Environments, ",") != "acme/production" || strings.Join(note.Services, ",") != "idp" {
		t.Errorf("folded = %v / %v", note.Environments, note.Services)
	}
}

func TestConsolidateEnvironmentPage(t *testing.T) {
	s := newService(t, nil)
	openProject(t, s)
	logSubject(t, s, "proj", store.ScopeEnvironment, "acme/production", "", "public buckets are denied by policy")

	path, err := s.Consolidate(ConsolidateRequest{TargetID: "proj", Environment: "acme/production",
		Body: "## What we know\n\nPublic buckets are denied.", Checked: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if !strings.HasSuffix(path, "notes/environments/acme-production.md") {
		t.Errorf("path = %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	for _, want := range []string{"kind: environment", "environment: acme/production", "Public buckets are denied."} {
		if !strings.Contains(string(data), want) {
			t.Errorf("page is missing %q:\n%s", want, data)
		}
	}

	// An entry logged once the page exists links to it.
	e := logSubject(t, s, "proj", store.ScopeEnvironment, "acme/production", "", "logging goes to a central account")
	if !strings.Contains(e.SubjectNote, "notes/environments/acme-production") {
		t.Errorf("subject_note = %q, want a link to the page", e.SubjectNote)
	}

	for _, req := range []ConsolidateRequest{
		{TargetID: "proj", Environment: "acme/production", Service: "idp", Body: "x"},
		{TargetID: "proj", Environment: "production", Body: "x"},
	} {
		if _, err := s.Consolidate(req); err == nil {
			t.Errorf("Consolidate(%+v) was accepted", req)
		}
	}
}
