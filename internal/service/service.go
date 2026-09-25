// Package service implements the five verbs. The MCP server and the CLI are both
// thin wrappers over it, so there is one write path and no drift between what an
// agent sees and what a person sees.
package service

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/bundle"
	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/identity"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// Transport says how the caller reached this process, which decides only one
// thing: where the host recorded on an entry comes from.
//
// Over stdio the host spawns one process per session, so the environment is the
// agent's and detecting the host from it is right — and it is the only way to
// tell Claude Code inside Kepler from Claude Code on its own, since they share
// one registration. Over SSE one long-lived server serves every client, so its
// environment is the menu bar app's; the name the client announced at
// initialize is used instead.
//
// Which task is meant is never inferred from either. It is a required argument.
type Transport string

// Transports.
const (
	TransportCLI   Transport = "cli"
	TransportStdio Transport = "stdio"
	TransportSSE   Transport = "sse"
)

// Service holds everything the verbs need.
type Service struct {
	Config   *config.Config
	Store    *store.Store
	Assembly *bundle.Assembler
	Env      identity.Env

	// Transport decides whether the environment may be trusted to identify the
	// caller. Defaults to CLI, which is the conservative reading for a process
	// someone started by hand.
	Transport Transport

	// CallerHost, when set, overrides host detection for a single call. Over
	// SSE this carries the MCP client's declared name, which is the only thing
	// that distinguishes one connected agent from another.
	CallerHost string

	// CallerSession is the caller's own session id. Over SSE the environment
	// holds the menu bar app's, which would be stamped on entries written by
	// every client and make the field worse than absent.
	CallerSession string

	now func() time.Time
}

// New builds a Service from configuration.
func New(cfg *config.Config) (*Service, error) {
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("configuration: %w", errors.Join(errs...))
	}

	s := store.New(cfg.ExpandedVaultPath())
	return &Service{
		Config:    cfg,
		Store:     s,
		Assembly:  bundle.New(s, cfg.Stale.MaxAgeDays),
		Env:       identity.OSEnv(),
		Transport: TransportCLI,
		now:       time.Now,
	}, nil
}

// WithTransport returns the service tagged with how the caller reached it.
func (s *Service) WithTransport(t Transport) *Service {
	s.Transport = t
	return s
}

// WithCallerHost returns the service tagged with the caller's declared identity.
func (s *Service) WithCallerHost(host string) *Service {
	s.CallerHost = host
	return s
}

// WithCallerSession returns the service tagged with the caller's session id.
func (s *Service) WithCallerSession(id string) *Service {
	s.CallerSession = id
	return s
}

// session returns the session id to stamp on an entry.
func (s *Service) session() string {
	if s.Transport == TransportSSE {
		return s.CallerSession
	}
	if s.CallerSession != "" {
		return s.CallerSession
	}
	return s.Env.Session()
}

// SetClock overrides the clock. Tests only.
func (s *Service) SetClock(f func() time.Time) {
	s.now = f
	s.Store.SetClock(f)
	s.Assembly.Now = f
}

// host is the value stamped on every entry this call writes.
//
// Over SSE the process environment belongs to the menu bar app, so detection
// would stamp every entry with the same wrong answer and the question the log
// is meant to settle — which hosts actually write anything — could not be
// asked. The MCP client's declared name is used instead.
func (s *Service) host() string {
	if s.Transport == TransportSSE {
		if h := strings.TrimSpace(s.CallerHost); h != "" {
			return h
		}
		if h := strings.TrimSpace(s.Config.Host); h != "" {
			return h
		}
		return "sse-client"
	}
	return s.Env.Host(s.Config.Host)
}

// requireTaskID normalises the task id, which every verb that writes must be
// given.
//
// There used to be a resolution chain here: a recorded binding, the worktree
// path, a tracker key parsed out of the branch name. It is gone because of what
// it was worth against what it risked. It saved an agent from repeating a
// string that open_task had just printed at the top of its own output, and in
// exchange it could attribute an entry to the wrong task without anything
// looking wrong — the one mistake this system cannot detect or repair. A
// required parameter cannot make it.
func requireTaskID(explicit string) (string, error) {
	id := strings.ToLower(strings.TrimSpace(explicit))
	if id == "" {
		return "", errors.New("task_id is required")
	}
	return id, nil
}

// --- find ---

// FindRequest asks which projects and tasks are worth opening.
type FindRequest struct {
	Query         string
	Repo          string
	IncludeClosed bool
	ReadyOnly     bool
	Limit         int
}

// Find returns places to look: project and task ids, with why each matched.
//
// It exists because every other verb needs an id, and an agent that was not
// handed one has no other way to get it — it cannot list what is there, and
// guessing is what this design spent its effort removing. Find closes that
// loop without reopening it: it returns identifiers a caller then passes
// explicitly, not an inference made on their behalf.
func (s *Service) Find(req FindRequest) ([]store.Match, error) {
	return s.Store.Search(store.SearchFilter{
		Terms:         strings.Fields(req.Query),
		Repo:          req.Repo,
		IncludeClosed: req.IncludeClosed,
		ReadyOnly:     req.ReadyOnly,
		Limit:         req.Limit,
	})
}

// RenderMatches formats search results for an agent or a terminal.
func RenderMatches(matches []store.Match, req FindRequest) string {
	var out strings.Builder

	if len(matches) == 0 {
		out.WriteString("Nothing here matches.\n\n")
		switch {
		case req.Query != "" || req.Repo != "":
			out.WriteString("Try fewer words, or call find with no arguments to see everything. " +
				"Matching is literal, not semantic.\n")
		default:
			out.WriteString("The vault holds no projects yet. Create one with open_project " +
				"— projects are made deliberately, never inferred.\n")
		}
		return out.String()
	}

	out.WriteString("# Places to look\n\n")
	for _, m := range matches {
		if m.Kind == "project" {
			fmt.Fprintf(&out, "- **project `%s`**", m.ID)
			if m.Title != "" {
				fmt.Fprintf(&out, " — %s", m.Title)
			}
		} else {
			fmt.Fprintf(&out, "- **task `%s`**", m.ID)
			if m.Title != "" {
				fmt.Fprintf(&out, " — %s", m.Title)
			}
			fmt.Fprintf(&out, " (project `%s`", m.Project)
			switch {
			case !m.Open:
				out.WriteString(", closed")
			case len(m.Waiting) > 0:
				fmt.Fprintf(&out, ", blocked by %s", strings.Join(m.Waiting, ", "))
			}
			out.WriteString(")")
		}
		out.WriteString("\n")

		var meta []string
		if len(m.Repos) > 0 {
			meta = append(meta, strings.Join(m.Repos, ", "))
		}
		meta = append(meta, m.Why)
		if m.Entries > 0 {
			meta = append(meta, fmt.Sprintf("%d logged", m.Entries))
		}
		fmt.Fprintf(&out, "  <sub>%s</sub>\n", strings.Join(meta, " · "))

		if m.Snippet != "" {
			fmt.Fprintf(&out, "  > %s\n", m.Snippet)
		}
	}

	out.WriteString("\nOpen one to get what is actually recorded: open_task for a task, " +
		"open_project for a project.\n")
	return out.String()
}

// --- open_project ---

// OpenProjectRequest names a project and, on first call, describes it.
type OpenProjectRequest struct {
	ID               string
	Name             string
	JiraProjects     []string
	GitLabGroups     []string
	Repos            []string
	ConfluenceSpaces []string
}

// OpenProject returns a project bundle, creating the project note if it is
// missing.
//
// Creation needs the arguments because the key-prefix mapping cannot be guessed:
// it is what makes PROJ-142 resolve to this folder rather than another, and a
// wrong guess quietly splits one project's knowledge across two corpora.
func (s *Service) OpenProject(req OpenProjectRequest) (*bundle.ProjectBundle, error) {
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return nil, errors.New("project_id is required")
	}

	created := false
	if _, _, err := s.Store.ReadProject(id); err != nil {
		if !errors.Is(err, store.ErrNoProject) {
			return nil, err
		}
		if req.Name == "" && len(req.JiraProjects) == 0 && len(req.Repos) == 0 {
			return nil, fmt.Errorf("project %q does not exist yet: to create it, pass at least name "+
				"and the tracker keys or repos that belong to it", id)
		}
		note := &store.ProjectNote{
			ID: id, Name: req.Name,
			JiraProjects: req.JiraProjects, GitLabGroups: req.GitLabGroups,
			Repos: req.Repos, ConfluenceSpaces: req.ConfluenceSpaces,
		}
		if err := s.Store.CreateProject(note); err != nil {
			return nil, err
		}
		created = true

		if _, err := s.Store.Append(&store.Entry{
			Kind: store.KindOpened, Project: id, Scope: store.ScopeProject,
			Host: s.host(), Session: s.session(),
			Body: fmt.Sprintf("Project %q created.", id),
		}); err != nil {
			return nil, err
		}
	}

	b, err := s.Assembly.Project(id)
	if err != nil {
		return nil, err
	}
	b.Created = created
	return b, nil
}

// --- open_task ---

// Seed is what the agent looked up about a task. agentctx holds no tracker
// credentials, so this is the only way a task's identity enters the vault — and
// being a required argument on creation, it is enforced by the tool schema
// rather than by asking nicely.
type Seed struct {
	Source    string
	SourceKey string
	SourceURL string
	Title     string
	Repos     []string
}

// OpenTaskRequest asks for a task bundle.
type OpenTaskRequest struct {
	TaskID  string
	Project string
	Seed    *Seed
}

// OpenTask returns a task bundle, creating the task note if it is missing.
//
// It never rewrites an existing note. An open is recorded by appending an entry,
// which gives more than a last_opened field would — every open, with its host and
// time — and keeps a single writer per file, which is what makes parallel
// sessions on a synced vault safe.
func (s *Service) OpenTask(req OpenTaskRequest) (*bundle.TaskBundle, error) {
	taskID, err := requireTaskID(req.TaskID)
	if err != nil {
		return nil, err
	}

	project, err := s.resolveProject(taskID, req)
	if err != nil {
		return nil, err
	}

	created := false
	if !s.Store.TaskExists(project, taskID) {
		if req.Seed == nil || strings.TrimSpace(req.Seed.Title) == "" {
			return nil, fmt.Errorf("task %q does not exist yet and cannot be created without a seed: "+
				"look it up in the tracker and pass seed.title (plus source, source_key, source_url and "+
				"repos when it came from one). agentctx holds no tracker credentials by design", taskID)
		}
		source := req.Seed.Source
		if source == "" {
			if req.Seed.SourceKey != "" {
				source = "jira"
			} else {
				source = "manual"
			}
		}
		note := &store.TaskNote{
			ID: taskID, Project: project, Source: source,
			SourceKey: req.Seed.SourceKey, SourceURL: req.Seed.SourceURL,
			Title: req.Seed.Title, Repos: req.Seed.Repos,
		}
		if err := s.Store.CreateTask(note); err != nil {
			return nil, err
		}
		created = true
	}

	// The task's first fact is where it came from.
	openedBody := fmt.Sprintf("Opened on %s.", s.host())
	if created {
		openedBody = fmt.Sprintf("Task created from %s on %s.", describeSeed(req.Seed), s.host())
	}
	if _, err := s.Store.Append(&store.Entry{
		Kind: store.KindOpened, Task: taskID, Project: project, Scope: store.ScopeTask,
		Host: s.host(), Session: s.session(), Body: openedBody,
	}); err != nil {
		return nil, err
	}

	b, err := s.Assembly.Task(project, taskID)
	if err != nil {
		return nil, err
	}
	b.Created = created
	return b, nil
}

// resolveProject decides which project a task belongs to, and refuses rather
// than inventing one.
func (s *Service) resolveProject(taskID string, req OpenTaskRequest) (string, error) {
	if p := strings.TrimSpace(req.Project); p != "" {
		if _, _, err := s.Store.ReadProject(p); err != nil {
			return "", err
		}
		return p, nil
	}

	// An existing task settles it.
	if project, err := s.Store.FindTask(taskID); err == nil {
		return project, nil
	}

	key := taskID
	if req.Seed != nil && req.Seed.SourceKey != "" {
		key = req.Seed.SourceKey
	}
	project, err := s.Store.ResolveProjectForKey(key)
	if err != nil {
		return "", fmt.Errorf("%w — pass project explicitly, or create it with open_project", err)
	}
	return project, nil
}

func describeSeed(seed *Seed) string {
	if seed == nil {
		return "an unrecorded source"
	}
	switch {
	case seed.SourceURL != "":
		return seed.SourceURL
	case seed.SourceKey != "":
		return seed.SourceKey
	case seed.Source != "":
		return seed.Source
	default:
		return "a manual entry"
	}
}

// --- log_decision ---

// LogDecisionRequest records a choice and why it was made.
type LogDecisionRequest struct {
	TaskID               string
	Decision             string
	Rationale            string
	AlternativesRejected []string
	AffectedRepos        []string
	// Supersedes names entries this decision replaces. See Entry.Supersedes.
	Supersedes []string
}

// LogDecision appends a decision entry.
func (s *Service) LogDecision(req LogDecisionRequest) (*store.Entry, error) {
	if strings.TrimSpace(req.Decision) == "" {
		return nil, errors.New("decision is required")
	}
	if strings.TrimSpace(req.Rationale) == "" {
		return nil, errors.New("rationale is required: a decision without one cannot be re-evaluated later")
	}

	taskID, project, err := s.locateTask(req.TaskID)
	if err != nil {
		return nil, err
	}

	var body strings.Builder
	body.WriteString(strings.TrimSpace(req.Decision))
	fmt.Fprintf(&body, "\n\n**Rationale:** %s\n", strings.TrimSpace(req.Rationale))
	if len(req.AlternativesRejected) > 0 {
		body.WriteString("\n**Alternatives rejected:**\n")
		for _, alt := range req.AlternativesRejected {
			fmt.Fprintf(&body, "- %s\n", strings.TrimSpace(alt))
		}
	}
	if len(req.AffectedRepos) > 0 {
		fmt.Fprintf(&body, "\n**Affects:** %s\n", strings.Join(req.AffectedRepos, ", "))
	}

	entry := &store.Entry{
		Kind: store.KindDecision, Task: taskID, Project: project, Scope: store.ScopeTask,
		Host: s.host(), Session: s.session(), Body: body.String(),
	}
	if entry.Supersedes, err = s.checkSupersedes(project, req.Supersedes); err != nil {
		return nil, err
	}
	if _, err := s.Store.Append(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// --- log_finding ---

// LogFindingRequest records something looked up and confirmed.
type LogFindingRequest struct {
	TargetID   string
	Finding    string
	Source     store.Source
	Scope      string
	Repo       string
	Confidence string
	// Supersedes names entries this finding replaces. See Entry.Supersedes.
	Supersedes []string
}

// LogFinding appends a finding entry against a task or a project.
//
// Accepting a project as the target is what lets a read-only session — someone
// asking "what do we know about X" from Desktop — write back what it confirmed.
// The read side is also a write side.
func (s *Service) LogFinding(req LogFindingRequest) (*store.Entry, error) {
	if strings.TrimSpace(req.Finding) == "" {
		return nil, errors.New("finding is required")
	}
	if !store.ValidSourceKind(req.Source.Kind) {
		return nil, fmt.Errorf("source.kind %q is not one of %s",
			req.Source.Kind, strings.Join(store.SourceKinds(), ", "))
	}
	// Whatever the caller's forge calls it, one value is stored.
	req.Source.Kind = store.NormaliseSourceKind(req.Source.Kind)
	if req.Source.Kind == store.SourceFile {
		if err := store.ValidateFileRef(req.Source.Ref); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(req.Source.Retrieved) == "" {
		req.Source.Retrieved = s.now().Format(store.LayoutDate)
	}
	if _, err := store.ParseTime(req.Source.Retrieved); err != nil {
		return nil, fmt.Errorf("source.retrieved: %w (use YYYY-MM-DD)", err)
	}

	scope := req.Scope
	if scope == "" {
		scope = store.ScopeTask
	}
	if scope == store.ScopeRepo && strings.TrimSpace(req.Repo) == "" {
		return nil, errors.New("scope is repo, so repo is required: a repo-scoped finding with no repo " +
			"cannot be found again by the next task in that repository")
	}

	entry := &store.Entry{
		Kind: store.KindFinding, Scope: scope, Repo: req.Repo,
		Host: s.host(), Session: s.session(),
		Source:     &req.Source,
		Confidence: store.ResolveConfidence(req.Source.Kind, req.Confidence),
		Body:       strings.TrimSpace(req.Finding),
	}

	// A project target is legitimate, so try the task first and fall back.
	if taskID, project, err := s.locateTask(req.TargetID); err == nil {
		entry.Task, entry.Project = taskID, project
	} else {
		id := strings.TrimSpace(req.TargetID)
		if _, _, perr := s.Store.ReadProject(id); perr != nil {
			return nil, fmt.Errorf("target %q is neither a known task nor a known project: %w", req.TargetID, perr)
		}
		entry.Project = id
		if scope == store.ScopeTask {
			// A project target with no task cannot be task-scoped.
			entry.Scope = store.ScopeProject
		}
	}

	var err error
	if entry.Supersedes, err = s.checkSupersedes(entry.Project, req.Supersedes); err != nil {
		return nil, err
	}
	if _, err := s.Store.Append(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// checkSupersedes validates the entries a new one claims to replace and
// returns them as ids.
//
// Refused rather than recorded loosely, because a supersedes that names
// nothing hides nothing and the wrong entry stays in every bundle, while one
// that names the wrong entry silently hides a good one. Each must exist in the
// same project, be something an agent asserted, and not be replaced already:
// replacing a replaced entry would leave two live answers, so the caller is
// pointed at the current one instead.
func (s *Service) checkSupersedes(project string, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	entries, err := s.Store.Entries(project, store.EntryFilter{IncludeSuperseded: true})
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*store.Entry, len(entries))
	for _, e := range entries {
		byID[e.ID()] = e
	}

	var ids []string
	for _, ref := range refs {
		id := store.EntryID(strings.TrimSpace(ref))
		if id == "" {
			continue
		}
		old, ok := byID[id]
		switch {
		case !ok:
			return nil, fmt.Errorf("supersedes %q: no such entry in project %s — pass the id shown under the entry "+
				"in a bundle, or the file name log_finding returned", ref, project)
		case !store.Supersedable(old.Kind):
			return nil, fmt.Errorf("supersedes %q: a %s entry records that something happened and cannot be replaced; "+
				"only findings, decisions and notes can", ref, old.Kind)
		case old.SupersededBy != "":
			return nil, fmt.Errorf("supersedes %q: already replaced by %s — supersede that one instead", ref, old.SupersededBy)
		}
		ids = appendOnce(ids, id)
	}
	return ids, nil
}

func appendOnce(list []string, v string) []string {
	for _, have := range list {
		if have == v {
			return list
		}
	}
	return append(list, v)
}

// --- log_dependency ---

// LogDependencyRequest records that a task waits for others, or no longer does.
type LogDependencyRequest struct {
	TaskID    string
	DependsOn []string
	Reason    string
	Resolved  bool
}

// LogDependency appends a dependency entry.
//
// An event rather than a field on the task note. A stored blocked_by list is
// wrong the moment a blocker is finished and nobody edits the note; an entry is
// true forever about the moment it was written, and the current graph is folded
// out of the log, where a closed blocker stops blocking on its own.
func (s *Service) LogDependency(req LogDependencyRequest) (*store.Entry, error) {
	taskID, project, err := s.locateTask(req.TaskID)
	if err != nil {
		return nil, err
	}
	if len(req.DependsOn) == 0 {
		return nil, errors.New("depends_on is required: name at least one task this one waits for")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return nil, errors.New("reason is required: an ordering nobody can justify is one nobody can " +
			"safely remove later")
	}

	graph, err := s.Store.DependencyGraph(project)
	if err != nil {
		return nil, err
	}

	deps := make([]string, 0, len(req.DependsOn))
	for _, raw := range req.DependsOn {
		dep := strings.ToLower(strings.TrimSpace(raw))
		if dep == "" {
			continue
		}
		if dep == taskID {
			return nil, fmt.Errorf("%s cannot depend on itself", taskID)
		}

		// An unknown id is a typo far more often than it is a real blocker, and
		// a phantom one blocks work that nothing is actually waiting for. Open
		// it first: it needs a title anyway, and then there is somewhere for
		// what you learn about it to go.
		depProject, err := s.Store.FindTask(dep)
		if err != nil {
			return nil, fmt.Errorf("no task %q in the vault: open_task it first, "+
				"so a mistyped id cannot block this work forever", dep)
		}
		if depProject != project {
			return nil, fmt.Errorf("task %q belongs to project %q, not %q: "+
				"dependencies across projects are not modelled", dep, depProject, project)
		}

		if !req.Resolved {
			if cycle := graph.WouldCycle(taskID, dep); cycle != nil {
				return nil, fmt.Errorf("that would make a loop: %s. "+
					"A cycle leaves no task that can be started first — record what actually "+
					"blocks what, or resolve the edge going the other way",
					store.FormatCycle(cycle))
			}
			// Take the new edge into account for the rest of this call, so two
			// dependencies added at once cannot form a loop between them.
			graph.BlockedBy[taskID] = append(graph.BlockedBy[taskID], dep)
		}
		deps = append(deps, dep)
	}
	if len(deps) == 0 {
		return nil, errors.New("depends_on is required: name at least one task this one waits for")
	}

	var body strings.Builder
	if req.Resolved {
		fmt.Fprintf(&body, "No longer waiting for %s.\n\n", s.Store.TaskLinks(project, deps))
	} else {
		fmt.Fprintf(&body, "Waits for %s.\n\n", s.Store.TaskLinks(project, deps))
	}
	body.WriteString(strings.TrimSpace(req.Reason) + "\n")

	entry := &store.Entry{
		Kind: store.KindDependency, Task: taskID, Project: project, Scope: store.ScopeTask,
		Host: s.host(), Session: s.session(),
		DependsOn: deps, Resolved: req.Resolved,
		Body: body.String(),
	}
	if _, err := s.Store.Append(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// --- consolidate ---

// ConsolidateRequest folds logged entries into a note body.
type ConsolidateRequest struct {
	// TargetID is a task id or a project id.
	TargetID string

	// Repo, when set, targets that repository's note inside the target project
	// instead of the project note itself.
	Repo string

	// Body is the composed markdown. Written verbatim.
	Body string

	// ExpectThrough is the consolidated_through the caller read. Checked when
	// Checked is set, so two sessions folding the same note cannot silently
	// overwrite each other.
	ExpectThrough string
	Checked       bool
}

// Consolidate replaces a note body with a composed view of the log.
//
// The only verb that rewrites anything. Everything else appends, which is what
// makes parallel sessions safe on a synced vault; consolidation is the
// deliberate exception, and it is why the guard above exists.
//
// agentctx does not compose the prose. A mechanical fold would only concatenate
// entries, and the value of consolidating is judgement — dropping what was
// superseded, keeping the newest with its provenance, saying what is now
// understood rather than what was said. That is the caller's work; this writes
// the result and records how far the log has been folded.
func (s *Service) Consolidate(req ConsolidateRequest) (string, error) {
	target := strings.ToLower(strings.TrimSpace(req.TargetID))
	if target == "" {
		return "", errors.New("target_id is required: the task or project whose note this is")
	}
	body := store.TrimBody(req.Body)
	if body == "" {
		return "", errors.New("body is required: consolidating to nothing would delete what the note says")
	}

	// A task target.
	if project, err := s.Store.FindTask(target); err == nil {
		if req.Repo != "" {
			return "", errors.New("repo is for folding a repository's findings, which belong to a " +
				"project rather than a task — pass the project id as target_id")
		}

		// Record the fold before marking how far it reached, so the record is
		// itself covered. The other way round, every consolidation leaves its
		// own note dangling as unconsolidated, for ever.
		s.recordConsolidation(project, target, "task "+target)

		through, err := s.Store.NewestEntry(project, store.EntryFilter{Task: target})
		if err != nil {
			return "", err
		}
		if err := s.Store.ConsolidateTask(project, target, body, through, req.ExpectThrough, req.Checked); err != nil {
			return "", err
		}
		return s.Store.TaskPath(project, target), nil
	}

	// A project target, either its own note or one of its repositories'.
	if _, _, err := s.Store.ReadProject(target); err != nil {
		return "", fmt.Errorf("target %q is neither a known task nor a known project: %w", req.TargetID, err)
	}

	if repo := strings.TrimSpace(req.Repo); repo != "" {
		through, err := s.Store.NewestEntry(target, store.EntryFilter{
			Scope: store.ScopeRepo, Repos: []string{repo},
		})
		if err != nil {
			return "", err
		}
		if err := s.Store.ConsolidateRepo(target, repo, body, through, req.ExpectThrough, req.Checked); err != nil {
			return "", err
		}
		s.recordConsolidation(target, "", "repo note for "+repo)
		return s.Store.RepoNotePath(target, repo), nil
	}

	through, err := s.Store.NewestEntry(target, store.EntryFilter{Scope: store.ScopeProject})
	if err != nil {
		return "", err
	}
	if err := s.Store.ConsolidateProject(target, body, through, req.ExpectThrough, req.Checked); err != nil {
		return "", err
	}
	s.recordConsolidation(target, "", "project "+target)
	return s.Store.ProjectPath(target), nil
}

// recordConsolidation appends a note entry saying a fold happened.
//
// The log stays the record of everything, including of its own compaction: a
// rewritten note is the only thing here that loses information, so when it
// happened and how far it went belongs in the one place that never does.
func (s *Service) recordConsolidation(project, task, what string) {
	body := fmt.Sprintf("Consolidated %s. Everything logged before this entry is now in the note body.", what)

	if _, err := s.Store.Append(&store.Entry{
		Kind: store.KindNote, Task: task, Project: project, Scope: store.ScopeTask,
		Host: s.host(), Session: s.session(), Consolidation: true, Body: body,
	}); err != nil {
		// Not fatal: the note itself is written, and failing the call now would
		// invite a second fold over the first.
		slog.Warn("could not record the consolidation", "project", project, "err", err)
	}
}

// --- close_task ---

// CloseTaskRequest closes a task with a summary.
type CloseTaskRequest struct {
	TaskID  string
	Summary string
}

// ErrNothingLogged is the refusal that makes the contract real.
var ErrNothingLogged = errors.New("nothing was logged during this task")

// CloseTask appends a closed entry, refusing if the session recorded nothing.
//
// The refusal is the enforcement mechanism. An instruction is advice a model may
// weigh against everything else in its context; a tool that returns an error and
// says what is missing is a fact it has to deal with. It also fails in the right
// direction — the work is done either way, and the cost of refusing is one more
// tool call.
func (s *Service) CloseTask(req CloseTaskRequest) (*store.Entry, error) {
	if strings.TrimSpace(req.Summary) == "" {
		return nil, errors.New("summary is required")
	}

	taskID, project, err := s.locateTask(req.TaskID)
	if err != nil {
		return nil, err
	}

	entries, err := s.Store.Entries(project, store.EntryFilter{Task: taskID})
	if err != nil {
		return nil, err
	}

	var lastOpened time.Time
	for _, e := range entries {
		if e.Kind == store.KindOpened {
			if t, err := store.ParseTime(e.TS); err == nil && t.After(lastOpened) {
				lastOpened = t
			}
		}
	}

	logged := 0
	for _, e := range entries {
		if e.Kind != store.KindDecision && e.Kind != store.KindFinding {
			continue
		}
		if t, err := store.ParseTime(e.TS); err == nil && !t.Before(lastOpened) {
			logged++
		}
	}
	if logged == 0 {
		return nil, fmt.Errorf("%w: log_decision or log_finding at least once before closing %s. "+
			"If the work genuinely produced nothing worth keeping, say why in a finding with "+
			"source.kind=conversation — that is itself worth knowing", ErrNothingLogged, taskID)
	}

	entry := &store.Entry{
		Kind: store.KindClosed, Task: taskID, Project: project, Scope: store.ScopeTask,
		Host: s.host(), Session: s.session(), Body: strings.TrimSpace(req.Summary),
	}
	if _, err := s.Store.Append(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

// locateTask finds the project holding a task.
func (s *Service) locateTask(explicit string) (taskID, project string, err error) {
	taskID, err = requireTaskID(explicit)
	if err != nil {
		return "", "", err
	}

	project, err = s.Store.FindTask(taskID)
	if err != nil {
		return "", "", fmt.Errorf("%w — call open_task first so the note exists", err)
	}
	return taskID, project, nil
}
