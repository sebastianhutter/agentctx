package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// handlers carries what every tool call needs. A struct rather than globals, so
// the config path a caller passed on the command line is the one that is used.
type handlers struct {
	configPath string
	transport  service.Transport
}

// newService loads configuration per call. A call is rare and a vault is small,
// so this costs nothing and means an edited config takes effect without
// restarting every host that has the server registered.
//
// The context is not decoration: over SSE it carries the MCP session, and the
// session carries the name the client announced at initialize. That name is the
// only thing distinguishing two agents connected to one shared server, and it
// is what every entry written during the call is stamped with.
func (h *handlers) newService(ctx context.Context) (*service.Service, error) {
	cfg, err := config.Load(h.configPath)
	if err != nil {
		return nil, err
	}
	svc, err := service.New(cfg)
	if err != nil {
		return nil, err
	}
	name, session := callerIdentity(ctx)
	return svc.WithTransport(h.transport).WithCallerHost(name).WithCallerSession(session), nil
}

// callerIdentity returns the connected client's declared name and its session
// id, or empty strings when the transport carries neither.
func callerIdentity(ctx context.Context) (name, sessionID string) {
	session := server.ClientSessionFromContext(ctx)
	if session == nil {
		return "", ""
	}
	sessionID = session.SessionID()

	withInfo, ok := session.(server.SessionWithClientInfo)
	if !ok {
		return "", sessionID
	}
	info := withInfo.GetClientInfo()
	switch {
	case info.Name == "":
		return "", sessionID
	case info.Version != "":
		return info.Name + "/" + info.Version, sessionID
	default:
		return info.Name, sessionID
	}
}

// --- find ---

var findTool = mcp.NewTool("find",
	mcp.WithDescription(
		"Which project or task is this work? Call it when you have not been given an id.\n\n"+
			"Every other tool here needs one, and this is where it comes from — nothing is "+
			"inferred from your working directory or branch, so an id you were not told has to "+
			"be looked up.\n\n"+
			"Returns places to look, not what is recorded there: ids, titles, and why each "+
			"matched. Open one to get the actual knowledge. With no arguments it lists "+
			"everything, which is the fastest way to see whether this work has a project at all.\n\n"+
			"Matching is literal and case-insensitive, over ids, titles, repositories, "+
			"environments, services, tracker keys and the text of anything logged — so a query describing a problem can surface "+
			"the task where it was solved. It is not a semantic search; for that, search the "+
			"vault with whatever retrieval tool indexes it."),
	mcp.WithString("query",
		mcp.Description("Words that must all appear. Omit to list everything.")),
	mcp.WithString("repo",
		mcp.Description("Restrict to projects and tasks touching this repository, e.g. \"acme/infrastructure\".")),
	mcp.WithBoolean("ready_only",
		mcp.Description("Only tasks that can be started now — nothing unfinished is waiting in their way.")),
	mcp.WithBoolean("include_closed",
		mcp.Description("Include closed tasks. Off by default — a closed task is rarely the one you want to log against.")),
	mcp.WithNumber("limit",
		mcp.Description("Maximum results (default 20).")),
)

func (h *handlers) find(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	req := service.FindRequest{
		Query:         request.GetString("query", ""),
		Repo:          request.GetString("repo", ""),
		IncludeClosed: request.GetBool("include_closed", false),
		ReadyOnly:     request.GetBool("ready_only", false),
		Limit:         request.GetInt("limit", 0),
	}

	matches, err := svc.Find(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(service.RenderMatches(matches, req)), nil
}

// --- open_project ---

var openProjectTool = mcp.NewTool("open_project",
	mcp.WithDescription(
		"What do we know about this project? Returns project-scoped findings, findings per "+
			"repository, environment and service, what other projects know about those, and the "+
			"list of open tasks.\n\n"+
			"Creates the project note on first call, which needs the arguments below: the tracker "+
			"keys are what let a later open_task resolve PROJ-142 to this project, and they cannot "+
			"be guessed. Projects are deliberate and few; tasks are created lazily and are many."),
	mcp.WithString("project_id", mcp.Required(),
		mcp.Description("Short lower-case project id, e.g. \"proj\". Also the folder name in the vault.")),
	mcp.WithString("name",
		mcp.Description("Human-readable project name. Required when creating.")),
	mcp.WithArray("jira_projects", mcp.WithStringItems(),
		mcp.Description("Tracker project keys belonging to this project, e.g. [\"PROJ\"]. "+
			"This is the prefix-to-project mapping open_task uses.")),
	mcp.WithArray("gitlab_groups", mcp.WithStringItems(),
		mcp.Description("GitLab groups belonging to this project, e.g. [\"acme\"].")),
	mcp.WithArray("repos", mcp.WithStringItems(),
		mcp.Description("Repositories this project owns, e.g. [\"acme/infrastructure\"]. "+
			"Repo-scoped findings are collected per entry here.")),
	mcp.WithArray("environments", mcp.WithStringItems(),
		mcp.Description("Environments the project deploys to or runs in, by owner and name, e.g. "+
			"[\"acme/staging\", \"acme/production\"]. The owner is required: every organisation has a "+
			"production.")),
	mcp.WithArray("services", mcp.WithStringItems(),
		mcp.Description("Services the project depends on: a SaaS product by its name, e.g. [\"idp\"], "+
			"or a self-hosted instance by owner and name, e.g. [\"acme/idp\"].")),
	mcp.WithArray("confluence_spaces", mcp.WithStringItems(),
		mcp.Description("Wiki spaces belonging to this project.")),
)

func (h *handlers) openProject(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	id, err := request.RequireString("project_id")
	if err != nil {
		return mcp.NewToolResultError("project_id is required"), nil
	}

	b, err := svc.OpenProject(service.OpenProjectRequest{
		ID:               id,
		Name:             request.GetString("name", ""),
		JiraProjects:     request.GetStringSlice("jira_projects", nil),
		GitLabGroups:     request.GetStringSlice("gitlab_groups", nil),
		Repos:            request.GetStringSlice("repos", nil),
		Environments:     request.GetStringSlice("environments", nil),
		Services:         request.GetStringSlice("services", nil),
		ConfluenceSpaces: request.GetStringSlice("confluence_spaces", nil),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(b.Render()), nil
}

// --- open_task ---

var openTaskTool = mcp.NewTool("open_task",
	mcp.WithDescription(
		"Call this before reading code. Returns everything already known about a task, the "+
			"project it belongs to, the repositories it touches and the environments and services "+
			"its project works with, plus the working agreement. What other projects learned "+
			"about the same repositories, environments and services comes back too, as headlines "+
			"marked with the project they came from.\n\n"+
			"The task id is always given, never guessed: it is usually the tracker key in lower "+
			"case, and it is the first line of what this returns, so later calls can quote it.\n\n"+
			"On a task that does not exist yet, pass seed_title (and seed_source_key / seed_url / "+
			"seed_repos when the task came from a tracker). This server holds no tracker credentials "+
			"by design: look the issue up with your own tools and pass what you found. The task's "+
			"first recorded fact is where it came from."),
	mcp.WithString("task_id", mcp.Required(),
		mcp.Description("Task id, e.g. \"proj-142\". Lower case; usually the tracker key.")),
	mcp.WithString("project",
		mcp.Description("Project id. Only needed when the task id carries no known key prefix.")),
	mcp.WithString("seed_title",
		mcp.Description("The task's title. Required when the task note does not exist yet.")),
	mcp.WithString("seed_source", mcp.Enum("jira", "github", "gitlab", "manual"),
		mcp.Description("Which tracker or forge the task came from. Defaults to jira when a source key is given, else manual.")),
	mcp.WithString("seed_source_key",
		mcp.Description("Tracker key, e.g. \"PROJ-142\".")),
	mcp.WithString("seed_url",
		mcp.Description("Link to the issue or merge request.")),
	mcp.WithArray("seed_repos", mcp.WithStringItems(),
		mcp.Description("Repositories this task touches. Determines which repo-scoped findings come back.")),
)

func (h *handlers) openTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	taskID, err := request.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultError("task_id is required"), nil
	}

	req := service.OpenTaskRequest{
		TaskID:  taskID,
		Project: request.GetString("project", ""),
	}
	if title := request.GetString("seed_title", ""); title != "" {
		req.Seed = &service.Seed{
			Title:     title,
			Source:    request.GetString("seed_source", ""),
			SourceKey: request.GetString("seed_source_key", ""),
			SourceURL: request.GetString("seed_url", ""),
			Repos:     request.GetStringSlice("seed_repos", nil),
		}
	}

	b, err := svc.OpenTask(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(b.Render()), nil
}

// --- log_decision ---

var logDecisionTool = mcp.NewTool("log_decision",
	mcp.WithDescription(
		"Record a choice and why it was made, at the moment it is made — not at the end of the "+
			"session, which usually never arrives.\n\n"+
			"Worth a call whenever you picked one option over another and someone could reasonably "+
			"ask why in three months. The rationale is required: a decision without one cannot be "+
			"re-evaluated when the circumstances change, only second-guessed."),
	mcp.WithString("decision", mcp.Required(),
		mcp.Description("What was decided, in one or two sentences.")),
	mcp.WithString("rationale", mcp.Required(),
		mcp.Description("Why. The constraint or evidence that made this the choice.")),
	mcp.WithArray("alternatives_rejected", mcp.WithStringItems(),
		mcp.Description("What was considered and dropped, each with the reason it lost.")),
	mcp.WithArray("affected_repos", mcp.WithStringItems(),
		mcp.Description("Repositories this decision changes.")),
	mcp.WithArray("supersedes", mcp.WithStringItems(),
		mcp.Description("Ids of entries this one replaces — the `id` shown under an entry in a bundle. "+
			"Use it when something recorded earlier is wrong or was filed wrongly (wrong repo name, "+
			"wrong scope): the log is append-only, so the old entry is not edited, but it stops being "+
			"returned once replaced. Same project only.")),
	mcp.WithString("task_id", mcp.Required(),
		mcp.Description("Task id, as passed to open_task.")),
)

func (h *handlers) logDecision(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	decision, err := request.RequireString("decision")
	if err != nil {
		return mcp.NewToolResultError("decision is required"), nil
	}
	rationale, err := request.RequireString("rationale")
	if err != nil {
		return mcp.NewToolResultError("rationale is required"), nil
	}

	taskID, err := request.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultError("task_id is required (the one open_task printed)"), nil
	}

	entry, err := svc.LogDecision(service.LogDecisionRequest{
		TaskID:               taskID,
		Decision:             decision,
		Rationale:            rationale,
		AlternativesRejected: request.GetStringSlice("alternatives_rejected", nil),
		AffectedRepos:        request.GetStringSlice("affected_repos", nil),
		Supersedes:           request.GetStringSlice("supersedes", nil),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Logged decision on %s.%s\n%s", entry.Task, replaced(entry), entry.Path)), nil
}

// --- log_finding ---

var logFindingTool = mcp.NewTool("log_finding",
	mcp.WithDescription(
		"Record something you looked up and confirmed, with where it came from.\n\n"+
			"Scope is the important argument. Everything but scope=task outlives the task, and is "+
			"what makes the next piece of work cheaper. If the finding would help someone who has "+
			"never heard of this task, it is not task-scoped — prefer the wider scope when in doubt. "+
			"Choose the narrowest thing it is true of:\n"+
			"repo — a repository's code, build, tests, conventions.\n"+
			"environment — a deployment environment: an account, a cluster, a stage. What was "+
			"learned about it (a policy, a limit, a quirk), not an inventory of what runs there.\n"+
			"service — a service the work depends on: an API's behaviour, a limit, a configuration.\n"+
			"project — the project as a whole, when none of the above is the subject.\n"+
			"Repo, environment and service findings are also shown to other projects that work with "+
			"the same one.\n\n"+
			"Provenance is required. Confidence is derived from the source kind rather than asserted: "+
			"a file at a known commit can be re-checked, a conversation cannot, and an inference is "+
			"not evidence. You may pass a lower confidence than the derived one, never a higher.\n\n"+
			"The target may be a project as well as a task, so a session that is only answering "+
			"questions can still write back what it confirmed."),
	mcp.WithString("finding", mcp.Required(),
		mcp.Description("What is true, stated so it makes sense to someone who was not here.")),
	mcp.WithString("source_kind", mcp.Required(),
		mcp.Enum(store.SourceKinds()...),
		mcp.Description("Where it came from, strongest first.\n"+
			"file — source code at a commit; the only kind that can be re-read exactly.\n"+
			"merge-request — a proposed change under review, whatever your forge calls it: a "+
			"GitLab merge request or a GitHub pull request. Put the forge in the ref.\n"+
			"issue — a tracker issue: Jira, GitHub, Linear.\n"+
			"document — anything you can open again at a named place: a vault note, a PDF, a "+
			"spec, a plan. Ranks below file on purpose, because a plan describing code is only "+
			"as good as the code it was written from — cite the code when you have it.\n"+
			"confluence — a wiki page, which changes under you.\n"+
			"web — a URL, which rots, paywalls or changes with no version.\n"+
			"slack, conversation — someone said so; unrecheckable later.\n"+
			"inferred — reasoned, not verified. Never presented as fact.")),
	mcp.WithString("source_ref",
		mcp.Description("Pointer into the source. For source_kind=file this must pin a commit — "+
			"\"modules/vpc/main.tf@abc1234\", not \"@origin/main\", which moves and leaves the finding "+
			"unverifiable. For others: an issue key, a URL, a page title.")),
	mcp.WithString("source_retrieved",
		mcp.Description("Date you retrieved it, YYYY-MM-DD. Defaults to today. Drives the staleness marker.")),
	mcp.WithString("scope", mcp.Enum(store.ScopeTask, store.ScopeRepo, store.ScopeEnvironment, store.ScopeService, store.ScopeProject),
		mcp.Description("task | repo | environment | service | project. Defaults to task, which is the least useful — choose deliberately.")),
	mcp.WithString("repo",
		mcp.Description("Repository the finding is about. Required when scope=repo.")),
	mcp.WithString("environment",
		mcp.Description("Environment the finding is about, by owner and name, e.g. \"acme/production\". "+
			"Required when scope=environment, and only allowed then.")),
	mcp.WithString("service",
		mcp.Description("Service the finding is about: \"idp\" for the product, \"acme/idp\" for one "+
			"organisation's instance of it. Required when scope=service, and only allowed then.")),
	mcp.WithString("confidence", mcp.Enum(store.ConfidenceHigh, store.ConfidenceMedium, store.ConfidenceLow),
		mcp.Description("Optional. Only honoured when lower than the level derived from source_kind.")),
	mcp.WithArray("supersedes", mcp.WithStringItems(),
		mcp.Description("Ids of entries this one replaces — the `id` shown under an entry in a bundle. "+
			"Use it when something recorded earlier is wrong or was filed wrongly (wrong repo name, "+
			"wrong scope): the log is append-only, so the old entry is not edited, but it stops being "+
			"returned once replaced. Same project only.")),
	mcp.WithString("target_id", mcp.Required(),
		mcp.Description("Task id, or a project id when the finding is about the project as a whole.")),
)

func (h *handlers) logFinding(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	finding, err := request.RequireString("finding")
	if err != nil {
		return mcp.NewToolResultError("finding is required"), nil
	}
	sourceKind, err := request.RequireString("source_kind")
	if err != nil {
		return mcp.NewToolResultError("source_kind is required: a claim with no provenance cannot be re-verified"), nil
	}

	targetID, err := request.RequireString("target_id")
	if err != nil {
		return mcp.NewToolResultError("target_id is required: the task or project this is about"), nil
	}

	entry, err := svc.LogFinding(service.LogFindingRequest{
		TargetID: targetID,
		Finding:  finding,
		Source: store.Source{
			Kind:      sourceKind,
			Ref:       request.GetString("source_ref", ""),
			Retrieved: request.GetString("source_retrieved", ""),
		},
		Scope:       request.GetString("scope", ""),
		Repo:        request.GetString("repo", ""),
		Environment: request.GetString("environment", ""),
		Service:     request.GetString("service", ""),
		Confidence:  request.GetString("confidence", ""),
		Supersedes:  request.GetStringSlice("supersedes", nil),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	target := entry.Task
	if target == "" {
		target = entry.Project
	}
	return mcp.NewToolResultText(fmt.Sprintf("Logged %s-scoped finding on %s (confidence %s).%s\n%s",
		entry.Scope, target, entry.Confidence, replaced(entry), entry.Path)), nil
}

// replaced reports what an entry superseded, for the tool's reply.
func replaced(e *store.Entry) string {
	if len(e.Supersedes) == 0 {
		return ""
	}
	return " Replaces " + strings.Join(e.Supersedes, ", ") + ", which is no longer returned."
}

// --- log_dependency ---

var logDependencyTool = mcp.NewTool("log_dependency",
	mcp.WithDescription(
		"Record that this task cannot proceed until others are done, or that it no longer "+
			"waits for them.\n\n"+
			"Worth a call the moment you discover an ordering — usually while reading code, not "+
			"while planning. What it buys is that the next session, which will not have read what "+
			"you just read, is told before it starts work it cannot finish.\n\n"+
			"A dependency is an event, not a field: nothing is edited, and a blocker stops "+
			"blocking by being closed, so a finished dependency never needs clearing. Pass "+
			"resolved=true only when the ordering itself turns out to be wrong.\n\n"+
			"Both tasks must already exist — open_task the other one first. An unknown id is a "+
			"typo far more often than a real blocker, and a phantom blocker holds up work that "+
			"nothing is actually waiting for. Loops are refused, because a cycle leaves no task "+
			"that can be started first."),
	mcp.WithString("task_id", mcp.Required(),
		mcp.Description("The task that has to wait.")),
	mcp.WithArray("depends_on", mcp.Required(), mcp.WithStringItems(),
		mcp.Description("Task ids it waits for, e.g. [\"proj-139\"].")),
	mcp.WithString("reason", mcp.Required(),
		mcp.Description("Why this ordering exists. An ordering nobody can justify is one nobody can safely remove.")),
	mcp.WithBoolean("resolved",
		mcp.Description("Lift these edges instead of adding them — the ordering was wrong, not finished.")),
)

func (h *handlers) logDependency(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	taskID, err := request.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultError("task_id is required"), nil
	}
	dependsOn, err := request.RequireStringSlice("depends_on")
	if err != nil {
		return mcp.NewToolResultError("depends_on is required: name at least one task this one waits for"), nil
	}
	reason, err := request.RequireString("reason")
	if err != nil {
		return mcp.NewToolResultError("reason is required"), nil
	}

	entry, err := svc.LogDependency(service.LogDependencyRequest{
		TaskID: taskID, DependsOn: dependsOn, Reason: reason,
		Resolved: request.GetBool("resolved", false),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	verb := "now waits for"
	if entry.Resolved {
		verb = "no longer waits for"
	}
	return mcp.NewToolResultText(fmt.Sprintf("%s %s %s.\n%s",
		entry.Task, verb, strings.Join(entry.DependsOn, ", "), entry.Path)), nil
}

// --- consolidate ---

var consolidateTool = mcp.NewTool("consolidate",
	mcp.WithDescription(
		"Fold logged entries into a note's body, so the vault can be read by a person and not "+
			"only assembled by a tool.\n\n"+
			"This is the only tool that rewrites anything. Everything else appends, which is what "+
			"makes parallel sessions safe; this is the deliberate exception.\n\n"+
			"You compose the body — that is the work. A mechanical fold would just concatenate "+
			"entries; consolidating means judgement: drop what was superseded, keep the newest "+
			"claim with its provenance, and write what is now understood rather than what was "+
			"said. Read the entries from open_task or open_project first.\n\n"+
			"Targets: a task id for its note, a project id for the project note, or a project id "+
			"with one of repo, environment or service set for that subject's page under notes/ — "+
			"which is where findings from many tasks accumulate into something the next task "+
			"touching it reads.\n\n"+
			"Log entries are never edited or deleted. Consolidation is compaction, so anything "+
			"logged after the fold still comes back separately."),
	mcp.WithString("target_id", mcp.Required(),
		mcp.Description("A task id, or a project id.")),
	mcp.WithString("body", mcp.Required(),
		mcp.Description("The composed markdown body, written verbatim. Use the note's section headings.")),
	mcp.WithString("repo",
		mcp.Description("With a project target: fold that repository's findings into notes/<repo>.md instead.")),
	mcp.WithString("environment",
		mcp.Description("With a project target: fold that environment's findings into notes/environments/<id>.md instead.")),
	mcp.WithString("service",
		mcp.Description("With a project target: fold that service's findings into notes/services/<id>.md instead.")),
	mcp.WithString("expect_consolidated_through",
		mcp.Description("The consolidated_through you read. Refuses if it has moved since, so two "+
			"sessions folding at once cannot overwrite each other. Pass \"\" when the note has never "+
			"been consolidated; omit to skip the check.")),
)

func (h *handlers) consolidate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	targetID, err := request.RequireString("target_id")
	if err != nil {
		return mcp.NewToolResultError("target_id is required"), nil
	}
	body, err := request.RequireString("body")
	if err != nil {
		return mcp.NewToolResultError("body is required: it is the composed note, and this tool does not write it for you"), nil
	}

	req := service.ConsolidateRequest{
		TargetID:    targetID,
		Repo:        request.GetString("repo", ""),
		Environment: request.GetString("environment", ""),
		Service:     request.GetString("service", ""),
		Body:        body,
	}
	if raw, ok := request.GetArguments()["expect_consolidated_through"]; ok {
		if expect, ok := raw.(string); ok {
			req.ExpectThrough, req.Checked = expect, true
		}
	}

	path, err := svc.Consolidate(req)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText("Consolidated.\n" + path), nil
}

// --- close_task ---

var closeTaskTool = mcp.NewTool("close_task",
	mcp.WithDescription(
		"Close a task with a summary of what happened.\n\n"+
			"Refuses if no decision or finding was logged since the task was opened. That is "+
			"deliberate: a session that learned nothing worth recording is usually a session that "+
			"forgot to record it. If the work genuinely produced nothing, log that as a finding — "+
			"knowing an avenue was a dead end is worth keeping."),
	mcp.WithString("summary", mcp.Required(),
		mcp.Description("What happened, and what the next person should know.")),
	mcp.WithString("task_id", mcp.Required(),
		mcp.Description("Task id, as passed to open_task.")),
)

func (h *handlers) closeTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	summary, err := request.RequireString("summary")
	if err != nil {
		return mcp.NewToolResultError("summary is required"), nil
	}

	taskID, err := request.RequireString("task_id")
	if err != nil {
		return mcp.NewToolResultError("task_id is required (the one open_task printed)"), nil
	}

	entry, err := svc.CloseTask(service.CloseTaskRequest{
		TaskID:  taskID,
		Summary: summary,
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(strings.TrimSpace(fmt.Sprintf("Closed %s.\n%s", entry.Task, entry.Path))), nil
}

// --- amend_project ---

var amendProjectTool = mcp.NewTool("amend_project",
	mcp.WithDescription(
		"Add or remove a project's repositories, tracker keys, environments and services, when the work "+
			"shows the project is missing one. open_task lists such gaps under \"Project gaps\" with the "+
			"exact call.\n\n"+
			"A repository the project does not list is one whose findings the next task never sees. A tracker "+
			"key no project claims is one open_task cannot resolve without being told the project. Listing an "+
			"environment or a service brings what other projects know about it into this project's bundles "+
			"before anything has been logged about it here.\n\n"+
			"Each call is recorded in the log with its reason, so it can be undone by another. Claiming a key "+
			"is refused when another project claims it, when tasks in another project use it (a shared "+
			"tracker: keep passing project to open_task instead), or when no task in this project uses it yet."),
	mcp.WithString("project_id", mcp.Required(), mcp.Description("Project to amend.")),
	mcp.WithArray("add_repos", mcp.WithStringItems(),
		mcp.Description("Repositories that belong to the project, by their qualified name, e.g. \"acme/platform\".")),
	mcp.WithArray("remove_repos", mcp.WithStringItems(), mcp.Description("Repositories that do not belong after all.")),
	mcp.WithArray("add_tracker_keys", mcp.WithStringItems(),
		mcp.Description("Tracker key prefixes the project's tasks come from, e.g. [\"PROJ\"] for PROJ-142.")),
	mcp.WithArray("remove_tracker_keys", mcp.WithStringItems(), mcp.Description("Tracker keys to release.")),
	mcp.WithArray("add_environments", mcp.WithStringItems(),
		mcp.Description("Environments the project works with, by owner and name, e.g. [\"acme/production\"].")),
	mcp.WithArray("remove_environments", mcp.WithStringItems(), mcp.Description("Environments it does not work with after all.")),
	mcp.WithArray("add_services", mcp.WithStringItems(),
		mcp.Description("Services the project depends on, e.g. [\"idp\"] or [\"acme/idp\"].")),
	mcp.WithArray("remove_services", mcp.WithStringItems(), mcp.Description("Services it does not depend on after all.")),
	mcp.WithString("reason", mcp.Required(),
		mcp.Description("Why these belong, or do not: what showed it.")),
)

func (h *handlers) amendProject(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService(ctx)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	projectID, err := request.RequireString("project_id")
	if err != nil {
		return mcp.NewToolResultError("project_id is required"), nil
	}
	entry, err := svc.AmendProject(service.AmendProjectRequest{
		ProjectID:   projectID,
		AddRepos:    request.GetStringSlice("add_repos", nil),
		RemoveRepos: request.GetStringSlice("remove_repos", nil),
		AddKeys:     request.GetStringSlice("add_tracker_keys", nil),
		RemoveKeys:  request.GetStringSlice("remove_tracker_keys", nil),

		AddEnvironments:    request.GetStringSlice("add_environments", nil),
		RemoveEnvironments: request.GetStringSlice("remove_environments", nil),
		AddServices:        request.GetStringSlice("add_services", nil),
		RemoveServices:     request.GetStringSlice("remove_services", nil),
		Reason:             request.GetString("reason", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Amended project %s.\n%s\n%s", entry.Project, entry.Body, entry.Path)), nil
}
