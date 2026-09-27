package main

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	consolidateTarget string
	consolidateRepo   string
	consolidateEnv    string
	consolidateSvc    string
	consolidateFile   string
	consolidateExpect string
)

var consolidateCmd = &cobra.Command{
	Use:   "consolidate <target-id>",
	Short: "Fold logged entries into a note body",
	Long: `Replace a note's body with a composed view of what has been logged.

The body is read from --file, or from standard input. This is the only command
that rewrites anything; everything else appends.

Targets: a task id, a project id, or a project id with --repo, --environment or
--service for that subject's page under notes/.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}

		target := consolidateTarget
		if len(args) == 1 {
			target = args[0]
		}

		var body []byte
		if consolidateFile != "" {
			body, err = os.ReadFile(consolidateFile)
		} else {
			body, err = io.ReadAll(cmd.InOrStdin())
		}
		if err != nil {
			return fmt.Errorf("read body: %w", err)
		}

		req := service.ConsolidateRequest{
			TargetID:    target,
			Repo:        consolidateRepo,
			Environment: consolidateEnv,
			Service:     consolidateSvc,
			Body:        string(body),
		}
		if cmd.Flags().Changed("expect-through") {
			req.ExpectThrough, req.Checked = consolidateExpect, true
		}

		path, err := svc.Consolidate(req)
		if err != nil {
			return err
		}
		fmt.Printf("Consolidated\n%s\n", path)
		return nil
	},
}

func init() {
	consolidateCmd.Flags().StringVar(&consolidateTarget, "target", "", "Task or project id")
	consolidateCmd.Flags().StringVar(&consolidateRepo, "repo", "", "Fold this repository's findings instead")
	consolidateCmd.Flags().StringVar(&consolidateEnv, "environment", "", "Fold this environment's findings instead")
	consolidateCmd.Flags().StringVar(&consolidateSvc, "service", "", "Fold this service's findings instead")
	consolidateCmd.Flags().StringVar(&consolidateFile, "file", "", "Read the body from this file (default: stdin)")
	consolidateCmd.Flags().StringVar(&consolidateExpect, "expect-through", "", "Refuse if consolidated_through has moved")

	rootCmd.AddCommand(consolidateCmd)
}
