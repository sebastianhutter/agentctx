package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	amendAddRepos    []string
	amendRemoveRepos []string
	amendAddKeys     []string
	amendRemoveKeys  []string
	amendAddEnvs     []string
	amendRemoveEnvs  []string
	amendAddServices []string
	amendRemoveSvcs  []string
	amendReason      string
)

var amendProjectCmd = &cobra.Command{
	Use:   "amend-project <project-id>",
	Short: "Add or remove a project's repositories, tracker keys, environments and services",
	Long: `Record a change to what belongs to a project. The project note keeps what was
set at creation; amendments are log entries folded on top, so each keeps its
reason and a wrong one is undone by another.

A tracker key is refused when another project claims it, when tasks in another
project use it, or when no task in this project uses it yet.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		entry, err := svc.AmendProject(service.AmendProjectRequest{
			ProjectID: args[0], AddRepos: amendAddRepos, RemoveRepos: amendRemoveRepos,
			AddKeys: amendAddKeys, RemoveKeys: amendRemoveKeys,
			AddEnvironments: amendAddEnvs, RemoveEnvironments: amendRemoveEnvs,
			AddServices: amendAddServices, RemoveServices: amendRemoveSvcs,
			Reason: amendReason,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Amended project %s\n%s\n%s\n", entry.Project, entry.Body, entry.Path)
		return nil
	},
}

func init() {
	amendProjectCmd.Flags().StringSliceVar(&amendAddRepos, "add-repo", nil, "Repository that belongs to the project (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendRemoveRepos, "remove-repo", nil, "Repository that does not (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendAddKeys, "add-key", nil, "Tracker key prefix to claim, e.g. PROJ (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendRemoveKeys, "remove-key", nil, "Tracker key to release (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendAddEnvs, "add-environment", nil, "Environment the project works with, owner/name, e.g. acme/production (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendRemoveEnvs, "remove-environment", nil, "Environment it does not (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendAddServices, "add-service", nil, "Service the project depends on, e.g. idp or acme/idp (repeatable)")
	amendProjectCmd.Flags().StringSliceVar(&amendRemoveSvcs, "remove-service", nil, "Service it does not (repeatable)")
	amendProjectCmd.Flags().StringVar(&amendReason, "reason", "", "Why (required)")
	rootCmd.AddCommand(amendProjectCmd)
}
