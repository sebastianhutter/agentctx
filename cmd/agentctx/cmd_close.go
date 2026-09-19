package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	closeSummary string
	closeTaskID  string
)

var closeTaskCmd = &cobra.Command{
	Use:   "close-task",
	Short: "Close a task with a summary",
	Long: `Close a task.

Refuses if nothing was logged since the task was opened. A session that learned
nothing worth recording is usually a session that forgot to record it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		entry, err := svc.CloseTask(service.CloseTaskRequest{TaskID: closeTaskID, Summary: closeSummary})
		if err != nil {
			return err
		}
		fmt.Printf("Closed %s\n%s\n", entry.Task, entry.Path)
		return nil
	},
}

func init() {
	closeTaskCmd.Flags().StringVar(&closeSummary, "summary", "", "What happened (required)")
	closeTaskCmd.Flags().StringVar(&closeTaskID, "task", "", "Task id (default: the bound task)")
	_ = closeTaskCmd.MarkFlagRequired("summary")

	rootCmd.AddCommand(closeTaskCmd)
}
