package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	openTaskID      string
	openProjectID   string
	openSeedTitle   string
	openSeedSource  string
	openSeedKey     string
	openSeedURL     string
	openSeedRepos   []string
	openProjectName string
	openJiraKeys    []string
	openGitLabGroup []string
	openRepos       []string
	openSpaces      []string
)

var openTaskCmd = &cobra.Command{
	Use:   "open-task [task-id]",
	Short: "Print what is known about a task",
	Long: `Print the task bundle: what is known about this task, its project and its
repositories, plus the working agreement.

This is what a SessionStart hook should call. Its output goes to stdout so the
hook can inject it into the session; logging goes to stderr.

The task id is resolved from the argument, then from a binding recorded by an
earlier open-task in this worktree or Kepler task, then from a tracker key in the
branch name. A branch named after a task title carries no key, so pass the id
when you know it.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}

		id := openTaskID
		if len(args) == 1 {
			id = args[0]
		}

		req := service.OpenTaskRequest{TaskID: id, Project: openProjectID}
		if openSeedTitle != "" {
			req.Seed = &service.Seed{
				Title: openSeedTitle, Source: openSeedSource,
				SourceKey: openSeedKey, SourceURL: openSeedURL, Repos: openSeedRepos,
			}
		}

		b, err := svc.OpenTask(req)
		if err != nil {
			return err
		}
		fmt.Print(b.Render())
		return nil
	},
}

var openProjectCmd = &cobra.Command{
	Use:   "open-project <project-id>",
	Short: "Print what is known about a project, creating it if asked",
	Long: `Print the project bundle: project-scoped findings, per-repository findings and
the open tasks.

Creating a project needs --name and the tracker keys or repos that belong to it:
that mapping is what lets a later open-task resolve PROJ-142 to this project, and
it cannot be guessed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}

		b, err := svc.OpenProject(service.OpenProjectRequest{
			ID: args[0], Name: openProjectName,
			JiraProjects: openJiraKeys, GitLabGroups: openGitLabGroup,
			Repos: openRepos, ConfluenceSpaces: openSpaces,
		})
		if err != nil {
			return err
		}
		fmt.Print(b.Render())
		return nil
	},
}

func init() {
	openTaskCmd.Flags().StringVar(&openTaskID, "task", "", "Task id (alternative to the positional argument)")
	openTaskCmd.Flags().StringVar(&openProjectID, "project", "", "Project id, when the task key has no known prefix")
	openTaskCmd.Flags().StringVar(&openSeedTitle, "title", "", "Task title — required to create a task note")
	openTaskCmd.Flags().StringVar(&openSeedSource, "source", "", "jira | gitlab | manual")
	openTaskCmd.Flags().StringVar(&openSeedKey, "key", "", "Tracker key, e.g. PROJ-142")
	openTaskCmd.Flags().StringVar(&openSeedURL, "url", "", "Link to the issue or merge request")
	openTaskCmd.Flags().StringSliceVar(&openSeedRepos, "repo", nil, "Repository this task touches (repeatable)")

	openProjectCmd.Flags().StringVar(&openProjectName, "name", "", "Human-readable project name")
	openProjectCmd.Flags().StringSliceVar(&openJiraKeys, "jira-project", nil, "Tracker project key (repeatable)")
	openProjectCmd.Flags().StringSliceVar(&openGitLabGroup, "gitlab-group", nil, "GitLab group (repeatable)")
	openProjectCmd.Flags().StringSliceVar(&openRepos, "repo", nil, "Repository owned by this project (repeatable)")
	openProjectCmd.Flags().StringSliceVar(&openSpaces, "confluence-space", nil, "Wiki space (repeatable)")

	rootCmd.AddCommand(openTaskCmd, openProjectCmd)
}
