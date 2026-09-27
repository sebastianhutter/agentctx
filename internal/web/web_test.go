package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// newTestServer builds a server over a temp vault with a little content.
func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	vault, state := t.TempDir(), t.TempDir()

	cfgPath := filepath.Join(state, "config.json")
	cfg := &config.Config{
		VaultPath: vault, StateDir: state,
		Stale:      config.StaleConfig{MaxAgeDays: 30},
		Confidence: config.ConfidenceConfig{DeriveFromSource: true},
		GUI:        config.GUIConfig{MCPPort: 31124, WebPort: 31125},
	}
	if err := config.Save(cfg, cfgPath); err != nil {
		t.Fatalf("Save config: %v", err)
	}

	st := store.New(vault)
	if err := st.CreateProject(&store.ProjectNote{
		ID: "proj", Name: "Example project", Repos: []string{"acme/infrastructure"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.CreateTask(&store.TaskNote{
		ID: "proj-142", Project: "proj", Title: "Rework the network module",
		Repos: []string{"acme/infrastructure"},
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := st.CreateTask(&store.TaskNote{ID: "proj-143", Project: "proj", Title: "Blocked one"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	for _, e := range []*store.Entry{
		{Kind: store.KindDecision, Task: "proj-142", Project: "proj", Body: "Chose B"},
		{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeRepo, Repo: "acme/infrastructure",
			Body:   "The module breaks on upgrade",
			Source: &store.Source{Kind: store.SourceFile, Ref: "modules/vpc/main.tf@abc1234", Retrieved: "2026-09-19"}},
		{Kind: store.KindDependency, Task: "proj-143", Project: "proj",
			DependsOn: []string{"proj-142"}, Body: "Waits for [[proj-142]]."},
	} {
		if _, err := st.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	s, err := New(cfgPath, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, st
}

func get(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "localhost:31125"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Every page defines "content"; parsed into one set, the last file read answers
// for all of them and every URL renders the same page.
func TestEachPageRendersItself(t *testing.T) {
	s, _ := newTestServer(t)

	for _, tc := range []struct{ path, want string }{
		{"/", "Projects"},
		{"/project/proj", "Example project"},
		{"/project/proj/task/proj-142", "Rework the network module"},
		{"/project/proj/repo/acme/infrastructure", "acme/infrastructure"},
		{"/log", "Log"},
		{"/new", "New project"},
		{"/graph", "g-project"},
		{"/checks", "Checks"},
	} {
		rec := get(t, s, tc.path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", tc.path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("GET %s does not contain %q — pages may share a template set", tc.path, tc.want)
		}
	}
}

// The listener is on loopback, which stops another machine connecting. It does
// not stop a page resolving a name it controls to 127.0.0.1 and reading the
// vault through the browser.
func TestRejectsForeignHostHeader(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "attacker.example.com"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a foreign Host header got %d, want 403", rec.Code)
	}
}

// Ids from a URL become path segments.
func TestRejectsPathTraversal(t *testing.T) {
	s, _ := newTestServer(t)

	for _, path := range []string{
		"/project/" + url.PathEscape("../../etc"),
		"/project/proj/task/" + url.PathEscape("../../../etc/passwd"),
		"/project/" + url.PathEscape("..") + "/task/x",
	} {
		rec := get(t, s, path)
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s returned 200; it should not resolve", path)
		}
		if strings.Contains(rec.Body.String(), "root:") {
			t.Fatalf("GET %s leaked file contents", path)
		}
	}
}

func TestLogFilters(t *testing.T) {
	s, _ := newTestServer(t)

	all := get(t, s, "/log").Body.String()
	if !strings.Contains(all, "Chose B") || !strings.Contains(all, "breaks on upgrade") {
		t.Error("the unfiltered log is missing entries")
	}

	decisions := get(t, s, "/log?kind=decision").Body.String()
	if !strings.Contains(decisions, "Chose B") {
		t.Error("filtering to decisions lost the decision")
	}
	if strings.Contains(decisions, "breaks on upgrade") {
		t.Error("filtering to decisions kept a finding")
	}

	// Facets must survive being filtered on, or the form becomes a trapdoor.
	if !strings.Contains(decisions, `value="finding"`) {
		t.Error("the kind facet lost its other options once one was chosen")
	}
}

// A freshly created note carries the section skeleton, so plain emptiness is
// not the test for "has this ever been consolidated".
func TestUnconsolidatedNoteSaysSo(t *testing.T) {
	s, _ := newTestServer(t)

	body := get(t, s, "/project/proj/task/proj-142").Body.String()
	if !strings.Contains(body, "No consolidated body yet") {
		t.Error("a task note that is only a skeleton was presented as written")
	}
}

// The vault's own link syntax, which markdown does not know.
func TestWikilinksBecomeLinks(t *testing.T) {
	s, _ := newTestServer(t)

	body := get(t, s, "/project/proj/task/proj-143").Body.String()
	if strings.Contains(body, "[[proj-142]]") {
		t.Error("a wikilink rendered as literal brackets")
	}
	if !strings.Contains(body, `href="/t/proj-142"`) {
		t.Errorf("a wikilink did not become a link:\n%s", body)
	}

	rec := get(t, s, "/t/proj-142")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("/t/proj-142 = %d, want a redirect", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/project/proj/task/proj-142" {
		t.Errorf("Location = %q", loc)
	}
}

func TestBlockedTaskIsFlagged(t *testing.T) {
	s, _ := newTestServer(t)

	project := get(t, s, "/project/proj").Body.String()
	if !strings.Contains(project, "waiting for proj-142") {
		t.Error("the project page does not show what a blocked task waits for")
	}

	task := get(t, s, "/project/proj/task/proj-143").Body.String()
	if !strings.Contains(task, "Blocked") {
		t.Error("the task page does not lead with the block")
	}
}

func TestNewProjectForm(t *testing.T) {
	s, _ := newTestServer(t)

	form := url.Values{
		"project_id":    {"other"},
		"name":          {"Another project"},
		"jira_projects": {"OTHER\nSECOND"},
		"repos":         {"acme/website, acme/docs"},
	}
	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /new = %d, want a redirect: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/project/other" {
		t.Errorf("Location = %q", loc)
	}

	page := get(t, s, "/project/other").Body.String()
	// Both separators, because a textarea invites either.
	for _, want := range []string{"Another project", "OTHER", "acme/website", "acme/docs"} {
		if !strings.Contains(page, want) {
			t.Errorf("the created project is missing %q", want)
		}
	}
}

func TestNewProjectFormReportsFailure(t *testing.T) {
	s, _ := newTestServer(t)

	form := url.Values{"project_id": {"../escape"}, "name": {"Nope"}}
	req := httptest.NewRequest(http.MethodPost, "/new", strings.NewReader(form.Encode()))
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusSeeOther {
		t.Fatal("a climbing project id was created")
	}
	if !strings.Contains(rec.Body.String(), "invalid") {
		t.Errorf("the form did not report why it refused:\n%s", rec.Body.String())
	}
}

func TestMissingVaultIsReportedNotPanicked(t *testing.T) {
	state := t.TempDir()
	cfgPath := filepath.Join(state, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{"vault_path":"/nonexistent/vault","gui":{"mcp_port":31124,"web_port":31125}}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	s, err := New(cfgPath, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := get(t, s, "/")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a missing vault got %d, want 500 with an explanation", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "vault_path") {
		t.Errorf("the error does not say what is wrong:\n%s", rec.Body.String())
	}
}

func graphJSON(t *testing.T, s *Server, path string) graphData {
	t.Helper()
	rec := get(t, s, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	var data graphData
	if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return data
}

func hasEdge(data graphData, source, target, rel string) bool {
	for _, e := range data.Edges {
		if e.Source == source && e.Target == target && e.Rel == rel {
			return true
		}
	}
	return false
}

func nodeByID(data graphData, id string) *graphNode {
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			return &data.Nodes[i]
		}
	}
	return nil
}

func TestGraphJSON(t *testing.T) {
	s, st := newTestServer(t)
	// A second project on the same repository: the repo must be one node
	// joining both, which is the reason the graph exists.
	if err := st.CreateProject(&store.ProjectNote{ID: "other", Name: "Other", Repos: []string{"acme/infrastructure"}}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// And a repo written about without being listed.
	if _, err := st.Append(&store.Entry{Kind: store.KindFinding, Task: "proj-142", Project: "proj",
		Scope: store.ScopeRepo, Repo: "acme/platform", Body: "Unlisted",
		Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	data := graphJSON(t, s, "/graph.json")

	for _, want := range []struct{ source, target, rel string }{
		{"task:proj/proj-142", "project:proj", relIn},
		{"task:proj/proj-143", "project:proj", relIn},
		{"project:proj", "repo:acme/infrastructure", relOwns},
		{"project:other", "repo:acme/infrastructure", relOwns},
		{"project:proj", "repo:acme/platform", relUses},
		{"task:proj/proj-142", "repo:acme/infrastructure", relTouch},
		{"task:proj/proj-143", "task:proj/proj-142", relWaits},
	} {
		if !hasEdge(data, want.source, want.target, want.rel) {
			t.Errorf("missing edge %s -%s-> %s", want.source, want.rel, want.target)
		}
	}

	repo := nodeByID(data, "repo:acme/infrastructure")
	if repo == nil || repo.State != "shared-unconsolidated" || repo.Detail != "other, proj" {
		t.Errorf("shared repo node = %+v", repo)
	}
	if repo != nil && repo.Weight != 1 {
		t.Errorf("repo weight = %d, want its one repo-scoped finding", repo.Weight)
	}
	if n := nodeByID(data, "task:proj/proj-143"); n == nil || n.State != "blocked" {
		t.Errorf("proj-143 waits for an open task and should be blocked: %+v", n)
	}

	// Decisions and findings become entry nodes; the dependency entry does
	// not, since it is already the waits edge.
	var entries []graphNode
	for _, n := range data.Nodes {
		if n.Kind == nodeEntry {
			entries = append(entries, n)
		}
	}
	if len(entries) != 3 {
		t.Errorf("entry nodes = %d, want 3 (decision, repo finding, unlisted finding)", len(entries))
	}
	for _, e := range entries {
		if e.Label == store.KindDependency {
			t.Errorf("a dependency entry should be an edge, not a node: %+v", e)
		}
	}
}

func TestGraphJSONOneProject(t *testing.T) {
	s, st := newTestServer(t)
	if err := st.CreateProject(&store.ProjectNote{ID: "other", Name: "Other"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	data := graphJSON(t, s, "/graph.json?project=proj")
	if nodeByID(data, "project:other") != nil {
		t.Error("a project filter still returned another project")
	}
	if nodeByID(data, "project:proj") == nil {
		t.Error("the filtered project is missing")
	}

	rec := get(t, s, "/graph.json?project="+url.QueryEscape("../../etc"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a traversal id got %d, want 400", rec.Code)
	}
}

func TestChecksPageAndProjectIssues(t *testing.T) {
	s, st := newTestServer(t)
	// A repo written about but missing from the project note.
	if _, err := st.Append(&store.Entry{Kind: store.KindFinding, Task: "proj-142", Project: "proj",
		Scope: store.ScopeRepo, Repo: "acme/platform", Body: "Unlisted",
		Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	rec := get(t, s, "/checks")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /checks = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "acme/platform is used by") {
		t.Errorf("checks page does not report the unlisted repo")
	}
	if !strings.Contains(get(t, s, "/").Body.String(), "in the vault checks") {
		t.Error("index does not point at the warnings")
	}

	rec = get(t, s, "/project/proj")
	if !strings.Contains(rec.Body.String(), "for this project") {
		t.Error("project page does not show its checks")
	}
}

func TestGraphNodeDetail(t *testing.T) {
	s, st := newTestServer(t)
	entries, err := st.Entries("proj", store.EntryFilter{Task: "proj-142"})
	if err != nil || len(entries) == 0 {
		t.Fatalf("fixture entries: %v", err)
	}
	file := filepath.Base(entries[0].Path)

	for _, tc := range []struct{ query, want string }{
		{"kind=project&project=proj", "Example project"},
		{"kind=task&project=proj&task=proj-142", "Rework the network module"},
		// proj-142 is what proj-143 waits for, and the panel says so.
		{"kind=task&project=proj&task=proj-142", `data-node="task:proj/proj-143"`},
		{"kind=task&project=proj&task=proj-143", "blocked"},
		{"kind=repo&repo=" + url.QueryEscape("acme/infrastructure"), "The module breaks on upgrade"},
		{"kind=entry&project=proj&file=" + url.QueryEscape(file), "Chose B"},
	} {
		rec := get(t, s, "/graph/node?"+tc.query)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", tc.query, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s does not contain %q", tc.query, tc.want)
		}
	}

	for _, tc := range []struct {
		query string
		code  int
	}{
		{"kind=entry&project=proj&file=" + url.QueryEscape("../../etc/passwd"), http.StatusBadRequest},
		{"kind=task&project=" + url.QueryEscape("../etc") + "&task=x", http.StatusBadRequest},
		{"kind=repo&repo=" + url.QueryEscape("acme/nothing"), http.StatusNotFound},
		{"kind=task&project=proj&task=proj-999", http.StatusNotFound},
		{"kind=bogus", http.StatusNotFound},
	} {
		if rec := get(t, s, "/graph/node?"+tc.query); rec.Code != tc.code {
			t.Errorf("%s = %d, want %d", tc.query, rec.Code, tc.code)
		}
	}
}

func TestGraphJSONWarningsAndWeights(t *testing.T) {
	s, st := newTestServer(t)
	for i := 0; i < 2; i++ {
		if _, err := st.Append(&store.Entry{Kind: store.KindFinding, Task: "proj-142", Project: "proj",
			Scope: store.ScopeRepo, Repo: "acme/platform", Body: "Unlisted",
			Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	data := graphJSON(t, s, "/graph.json")

	if n := nodeByID(data, "repo:acme/platform"); n == nil || n.Warn == 0 {
		t.Errorf("an unlisted repo should carry its warning: %+v", n)
	}
	for _, e := range data.Edges {
		if e.Source == "task:proj/proj-142" && e.Target == "repo:acme/platform" && e.Rel == relTouch && e.Weight != 2 {
			t.Errorf("touches weight = %d, want 2 (two entries)", e.Weight)
		}
	}
	for _, n := range data.Nodes {
		if n.Kind == nodeEntry && (!strings.HasPrefix(n.ID, "entry:proj/") || n.File == "") {
			t.Errorf("entry node must name its project and file: %+v", n)
		}
	}
}

// Every page hides a replaced entry except the log, which is the history.
func TestSupersededEntriesInTheLogOnly(t *testing.T) {
	s, st := newTestServer(t)
	old := &store.Entry{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeRepo,
		Repo: "acme/infrastructure", Body: "Wrong claim",
		Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}}
	if _, err := st.Append(old); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := st.Append(&store.Entry{Kind: store.KindFinding, Project: "proj", Scope: store.ScopeRepo,
		Repo: "acme/infrastructure", Body: "Right claim", Supersedes: []string{old.ID()},
		Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	log := get(t, s, "/log").Body.String()
	if !strings.Contains(log, "Wrong claim") || !strings.Contains(log, "superseded by") {
		t.Error("the log view should keep the replaced entry, marked")
	}
	if strings.Contains(get(t, s, "/project/proj/repo/acme/infrastructure").Body.String(), "Wrong claim") {
		t.Error("the repo page still shows a replaced finding")
	}
}

// addSubjects gives the fixture a second project sharing an environment with
// the first, and a service only mentioned by an entry.
func addSubjects(t *testing.T, st *store.Store) {
	t.Helper()
	if err := st.CreateProject(&store.ProjectNote{
		ID: "other", Name: "Other", Environments: []string{"acme/production"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := st.CreateProject(&store.ProjectNote{
		ID: "third", Name: "Third", Environments: []string{"acme/staging"},
	}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	for _, e := range []*store.Entry{
		{Kind: store.KindFinding, Task: "proj-142", Project: "proj", Scope: store.ScopeEnvironment,
			Environment: "acme/production", Body: "Production runs two node pools",
			Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}},
		{Kind: store.KindFinding, Project: "other", Scope: store.ScopeEnvironment,
			Environment: "acme/production", Body: "Production has no NAT gateway",
			Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}},
		{Kind: store.KindFinding, Task: "proj-142", Project: "proj", Scope: store.ScopeService,
			Service: "idp", Body: "Tokens expire after an hour",
			Source: &store.Source{Kind: store.SourceInferred, Retrieved: "2026-09-19"}},
	} {
		if _, err := st.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// Environments and services join projects the way repos do.
func TestGraphJSONSubjects(t *testing.T) {
	s, st := newTestServer(t)
	addSubjects(t, st)
	data := graphJSON(t, s, "/graph.json")

	for _, want := range []struct{ source, target, rel string }{
		{"project:other", "environment:acme/production", relOwns},
		{"project:proj", "environment:acme/production", relUses},
		{"task:proj/proj-142", "environment:acme/production", relTouch},
		{"project:proj", "service:idp", relUses},
		{"task:proj/proj-142", "service:idp", relTouch},
		{"project:third", "environment:acme/staging", relOwns},
	} {
		if !hasEdge(data, want.source, want.target, want.rel) {
			t.Errorf("missing edge %s -%s-> %s", want.source, want.rel, want.target)
		}
	}

	env := nodeByID(data, "environment:acme/production")
	if env == nil || env.Kind != nodeEnvironment || env.State != "shared-unconsolidated" ||
		env.Detail != "other, proj" || env.Weight != 2 {
		t.Errorf("shared environment node = %+v", env)
	}
	if env != nil && env.URL != "/log?environment="+url.QueryEscape("acme/production") {
		t.Errorf("environment URL = %q", env.URL)
	}
	if n := nodeByID(data, "environment:acme/staging"); n == nil || n.State != "unconsolidated" || n.Weight != 0 {
		t.Errorf("listed, unwritten environment node = %+v", n)
	}
	svc := nodeByID(data, "service:idp")
	if svc == nil || svc.Kind != nodeService || svc.Detail != "proj" || svc.Weight != 1 {
		t.Errorf("service node = %+v", svc)
	}

	var about int
	for _, e := range data.Edges {
		if e.Rel == relAbout && (e.Target == "environment:acme/production" || e.Target == "service:idp") {
			about++
		}
	}
	if about != 3 {
		t.Errorf("about edges to subjects = %d, want 3", about)
	}
}

func TestGraphNodeSubjectDetail(t *testing.T) {
	s, st := newTestServer(t)
	addSubjects(t, st)

	for _, tc := range []struct{ query, want string }{
		{"kind=environment&environment=" + url.QueryEscape("acme/production"), "Production has no NAT gateway"},
		{"kind=environment&environment=" + url.QueryEscape("acme/production"), `data-node="project:other"`},
		{"kind=environment&environment=" + url.QueryEscape("acme/production"), "2 environment-scoped findings across 2 projects"},
		{"kind=service&service=idp", "Tokens expire after an hour"},
		// Listed but not yet written about still has a row.
		{"kind=environment&environment=" + url.QueryEscape("acme/staging"), `data-node="project:third"`},
	} {
		rec := get(t, s, "/graph/node?"+tc.query)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", tc.query, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s does not contain %q", tc.query, tc.want)
		}
	}

	for _, tc := range []struct {
		query string
		code  int
	}{
		{"kind=environment&environment=" + url.QueryEscape("../../etc/passwd"), http.StatusBadRequest},
		{"kind=service&service=" + url.QueryEscape("../etc"), http.StatusBadRequest},
		// An environment without its owner would join every production.
		{"kind=environment&environment=production", http.StatusBadRequest},
		{"kind=environment&environment=", http.StatusBadRequest},
		{"kind=service&service=nothing", http.StatusNotFound},
	} {
		if rec := get(t, s, "/graph/node?"+tc.query); rec.Code != tc.code {
			t.Errorf("%s = %d, want %d", tc.query, rec.Code, tc.code)
		}
	}
}

func TestLogFiltersBySubject(t *testing.T) {
	s, st := newTestServer(t)
	addSubjects(t, st)

	env := get(t, s, "/log?environment="+url.QueryEscape("acme/production")).Body.String()
	if !strings.Contains(env, "two node pools") || !strings.Contains(env, "no NAT gateway") {
		t.Error("the environment filter lost an entry about the environment")
	}
	if strings.Contains(env, "Tokens expire") || strings.Contains(env, "Chose B") {
		t.Error("the environment filter kept entries about something else")
	}
	// The graph folds case, so the link it makes must too.
	if !strings.Contains(get(t, s, "/log?environment="+url.QueryEscape("ACME/Production")).Body.String(), "no NAT gateway") {
		t.Error("the environment filter is case-sensitive")
	}

	svc := get(t, s, "/log?service=idp").Body.String()
	if !strings.Contains(svc, "Tokens expire") || strings.Contains(svc, "no NAT gateway") {
		t.Error("the service filter is wrong")
	}
	if !strings.Contains(svc, `value="environment"`) {
		t.Error("the scope facet lost its other options once a service was chosen")
	}
}

func TestProjectPageListsSubjects(t *testing.T) {
	s, st := newTestServer(t)
	addSubjects(t, st)

	page := get(t, s, "/project/proj").Body.String()
	for _, want := range []string{
		"Environments", `href="/project/proj/environment/acme/production"`,
		"Services", `href="/project/proj/service/idp"`,
		"Repositories", `href="/project/proj/repo/acme/infrastructure"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the project page is missing %q", want)
		}
	}
	if other := get(t, s, "/project/third").Body.String(); !strings.Contains(other, "acme/staging") ||
		!strings.Contains(other, "nothing recorded yet") {
		t.Error("a listed environment with no findings is missing from its project page")
	}

	rec := get(t, s, "/project/proj/environment/acme/production")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "two node pools") {
		t.Errorf("environment page = %d", rec.Code)
	}
	if rec := get(t, s, "/project/proj/service/idp"); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), "Tokens expire") {
		t.Errorf("service page = %d", rec.Code)
	}
	for _, path := range []string{
		"/project/proj/environment/" + url.PathEscape("../../etc/passwd"),
		"/project/proj/environment/production",
		"/project/" + url.PathEscape("../etc") + "/service/idp",
	} {
		if rec := get(t, s, path); rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "root:") {
			t.Errorf("GET %s = %d, want a refusal", path, rec.Code)
		}
	}
}
