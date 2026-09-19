package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// handlers carries what every tool call needs. A struct rather than globals, so
// the config path a caller passed on the command line is the one that is used.
type handlers struct {
	configPath string
}

// newService loads configuration per call. A call is rare and a vault is small,
// so this costs nothing and means an edited config takes effect without
// restarting every host that has the server registered.
func (h *handlers) newService() (*service.Service, error) {
	cfg, err := config.Load(h.configPath)
	if err != nil {
		return nil, err
	}
	return service.New(cfg)
}

// --- open_project ---

var openProjectTool = mcp.NewTool("open_project",
	mcp.WithDescription(
		"What do we know about this project? Returns project-scoped findings, per-repository "+
			"findings and the list of open tasks.\n\n"+
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
	mcp.WithArray("confluence_spaces", mcp.WithStringItems(),
		mcp.Description("Wiki spaces belonging to this project.")),
)

func (h *handlers) openProject(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService()
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
			"project it belongs to and the repositories it touches, plus the working agreement.\n\n"+
			"Task id resolution, in order: the task_id argument, a binding recorded by an earlier "+
			"open_task in this worktree or Kepler task, then a tracker key parsed out of the branch "+
			"name. The branch is often named after the task title rather than an issue, so pass "+
			"task_id when you know it.\n\n"+
			"On a task that does not exist yet, pass seed_title (and seed_source_key / seed_url / "+
			"seed_repos when the task came from a tracker). This server holds no tracker credentials "+
			"by design: look the issue up with your own tools and pass what you found. The task's "+
			"first recorded fact is where it came from."),
	mcp.WithString("task_id",
		mcp.Description("Task id, e.g. \"proj-142\". Omit only if an earlier open_task bound this worktree.")),
	mcp.WithString("project",
		mcp.Description("Project id. Only needed when the task id carries no known key prefix.")),
	mcp.WithString("seed_title",
		mcp.Description("The task's title. Required when the task note does not exist yet.")),
	mcp.WithString("seed_source", mcp.Enum("jira", "gitlab", "manual"),
		mcp.Description("Where the task came from. Defaults to jira when a source key is given, else manual.")),
	mcp.WithString("seed_source_key",
		mcp.Description("Tracker key, e.g. \"PROJ-142\".")),
	mcp.WithString("seed_url",
		mcp.Description("Link to the issue or merge request.")),
	mcp.WithArray("seed_repos", mcp.WithStringItems(),
		mcp.Description("Repositories this task touches. Determines which repo-scoped findings come back.")),
)

func (h *handlers) openTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	req := service.OpenTaskRequest{
		TaskID:  request.GetString("task_id", ""),
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
	mcp.WithString("task_id",
		mcp.Description("Task id. Omit to use the task bound by open_task.")),
)

func (h *handlers) logDecision(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService()
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

	entry, err := svc.LogDecision(service.LogDecisionRequest{
		TaskID:               request.GetString("task_id", ""),
		Decision:             decision,
		Rationale:            rationale,
		AlternativesRejected: request.GetStringSlice("alternatives_rejected", nil),
		AffectedRepos:        request.GetStringSlice("affected_repos", nil),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(fmt.Sprintf("Logged decision on %s.\n%s", entry.Task, entry.Path)), nil
}

// --- log_finding ---

var logFindingTool = mcp.NewTool("log_finding",
	mcp.WithDescription(
		"Record something you looked up and confirmed, with where it came from.\n\n"+
			"Scope is the important argument. scope=repo and scope=project outlive the task and are "+
			"what make the next piece of work cheaper; scope=task dies with this one. If the finding "+
			"would help someone who has never heard of this task, it is not task-scoped — prefer the "+
			"wider scope when in doubt.\n\n"+
			"Provenance is required. Confidence is derived from the source kind rather than asserted: "+
			"a file at a known commit can be re-checked, a conversation cannot, and an inference is "+
			"not evidence. You may pass a lower confidence than the derived one, never a higher.\n\n"+
			"The target may be a project as well as a task, so a session that is only answering "+
			"questions can still write back what it confirmed."),
	mcp.WithString("finding", mcp.Required(),
		mcp.Description("What is true, stated so it makes sense to someone who was not here.")),
	mcp.WithString("source_kind", mcp.Required(),
		mcp.Enum(store.SourceKinds()...),
		mcp.Description("Where it came from. file = source at a commit; inferred = reasoned, not verified.")),
	mcp.WithString("source_ref",
		mcp.Description("Pointer into the source: \"modules/vpc/main.tf@abc1234\", an issue key, a URL, a page title.")),
	mcp.WithString("source_retrieved",
		mcp.Description("Date you retrieved it, YYYY-MM-DD. Defaults to today. Drives the staleness marker.")),
	mcp.WithString("scope", mcp.Enum(store.ScopeTask, store.ScopeRepo, store.ScopeProject),
		mcp.Description("task | repo | project. Defaults to task, which is the least useful — choose deliberately.")),
	mcp.WithString("repo",
		mcp.Description("Repository the finding is about. Required when scope=repo.")),
	mcp.WithString("confidence", mcp.Enum(store.ConfidenceHigh, store.ConfidenceMedium, store.ConfidenceLow),
		mcp.Description("Optional. Only honoured when lower than the level derived from source_kind.")),
	mcp.WithString("target_id",
		mcp.Description("Task id or project id. Omit to use the task bound by open_task.")),
)

func (h *handlers) logFinding(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService()
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

	entry, err := svc.LogFinding(service.LogFindingRequest{
		TargetID: request.GetString("target_id", ""),
		Finding:  finding,
		Source: store.Source{
			Kind:      sourceKind,
			Ref:       request.GetString("source_ref", ""),
			Retrieved: request.GetString("source_retrieved", ""),
		},
		Scope:      request.GetString("scope", ""),
		Repo:       request.GetString("repo", ""),
		Confidence: request.GetString("confidence", ""),
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	target := entry.Task
	if target == "" {
		target = entry.Project
	}
	return mcp.NewToolResultText(fmt.Sprintf("Logged %s-scoped finding on %s (confidence %s).\n%s",
		entry.Scope, target, entry.Confidence, entry.Path)), nil
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
	mcp.WithString("task_id",
		mcp.Description("Task id. Omit to use the task bound by open_task.")),
)

func (h *handlers) closeTask(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	svc, err := h.newService()
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	summary, err := request.RequireString("summary")
	if err != nil {
		return mcp.NewToolResultError("summary is required"), nil
	}

	entry, err := svc.CloseTask(service.CloseTaskRequest{
		TaskID:  request.GetString("task_id", ""),
		Summary: summary,
	})
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(strings.TrimSpace(fmt.Sprintf("Closed %s.\n%s", entry.Task, entry.Path))), nil
}
