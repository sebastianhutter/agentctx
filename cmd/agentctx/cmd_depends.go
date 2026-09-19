package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	dependsTaskID   string
	dependsOn       []string
	dependsReason   string
	dependsResolved bool
)

var logDependencyCmd = &cobra.Command{
	Use:   "log-dependency",
	Short: "Record that a task waits for others",
	Long: `Record an ordering between tasks.

A dependency is an event, not a field: nothing is edited, and a blocker stops
blocking by being closed. Pass --resolved only when the ordering itself turns
out to be wrong.

Both tasks must exist, and loops are refused.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		entry, err := svc.LogDependency(service.LogDependencyRequest{
			TaskID: dependsTaskID, DependsOn: dependsOn,
			Reason: dependsReason, Resolved: dependsResolved,
		})
		if err != nil {
			return err
		}
		verb := "now waits for"
		if entry.Resolved {
			verb = "no longer waits for"
		}
		fmt.Printf("%s %s %s\n%s\n", entry.Task, verb, strings.Join(entry.DependsOn, ", "), entry.Path)
		return nil
	},
}

func init() {
	logDependencyCmd.Flags().StringVar(&dependsTaskID, "task", "", "The task that has to wait (required)")
	logDependencyCmd.Flags().StringSliceVar(&dependsOn, "depends-on", nil, "Task it waits for (repeatable, required)")
	logDependencyCmd.Flags().StringVar(&dependsReason, "reason", "", "Why this ordering exists (required)")
	logDependencyCmd.Flags().BoolVar(&dependsResolved, "resolved", false, "Lift these edges instead of adding them")
	_ = logDependencyCmd.MarkFlagRequired("task")
	_ = logDependencyCmd.MarkFlagRequired("depends-on")
	_ = logDependencyCmd.MarkFlagRequired("reason")

	rootCmd.AddCommand(logDependencyCmd)
}
