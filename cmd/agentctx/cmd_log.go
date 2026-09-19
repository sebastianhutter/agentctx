package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

var (
	decisionText    string
	rationaleText   string
	alternatives    []string
	affectedRepos   []string
	decisionTaskID  string
	findingText     string
	findingKind     string
	findingRef      string
	findingDate     string
	findingScope    string
	findingRepo     string
	findingConf     string
	findingTargetID string
)

var logDecisionCmd = &cobra.Command{
	Use:   "log-decision",
	Short: "Record a choice and why it was made",
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		entry, err := svc.LogDecision(service.LogDecisionRequest{
			TaskID: decisionTaskID, Decision: decisionText, Rationale: rationaleText,
			AlternativesRejected: alternatives, AffectedRepos: affectedRepos,
		})
		if err != nil {
			return err
		}
		fmt.Printf("Logged decision on %s\n%s\n", entry.Task, entry.Path)
		return nil
	},
}

var logFindingCmd = &cobra.Command{
	Use:   "log-finding",
	Short: "Record something looked up and confirmed",
	Long: `Record a finding with its provenance.

--scope is the argument that matters: repo and project outlive the task, task
does not. Confidence is derived from --source-kind; --confidence can only lower
it.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		entry, err := svc.LogFinding(service.LogFindingRequest{
			TargetID: findingTargetID, Finding: findingText,
			Source: store.Source{Kind: findingKind, Ref: findingRef, Retrieved: findingDate},
			Scope:  findingScope, Repo: findingRepo, Confidence: findingConf,
		})
		if err != nil {
			return err
		}
		target := entry.Task
		if target == "" {
			target = entry.Project
		}
		fmt.Printf("Logged %s-scoped finding on %s (confidence %s)\n%s\n",
			entry.Scope, target, entry.Confidence, entry.Path)
		return nil
	},
}

func init() {
	logDecisionCmd.Flags().StringVar(&decisionText, "decision", "", "What was decided (required)")
	logDecisionCmd.Flags().StringVar(&rationaleText, "rationale", "", "Why (required)")
	logDecisionCmd.Flags().StringSliceVar(&alternatives, "alternative", nil, "Option considered and dropped (repeatable)")
	logDecisionCmd.Flags().StringSliceVar(&affectedRepos, "repo", nil, "Repository this decision changes (repeatable)")
	logDecisionCmd.Flags().StringVar(&decisionTaskID, "task", "", "Task id (default: the bound task)")
	_ = logDecisionCmd.MarkFlagRequired("decision")
	_ = logDecisionCmd.MarkFlagRequired("rationale")

	logFindingCmd.Flags().StringVar(&findingText, "finding", "", "What is true (required)")
	logFindingCmd.Flags().StringVar(&findingKind, "source-kind", "", "file | gitlab-mr | jira | document | confluence | web | slack | conversation | inferred (required)")
	logFindingCmd.Flags().StringVar(&findingRef, "source-ref", "", "Pointer into the source, e.g. path@commit")
	logFindingCmd.Flags().StringVar(&findingDate, "retrieved", "", "Retrieval date YYYY-MM-DD (default today)")
	logFindingCmd.Flags().StringVar(&findingScope, "scope", "", "task | repo | project (default task)")
	logFindingCmd.Flags().StringVar(&findingRepo, "repo", "", "Repository the finding is about (required with --scope repo)")
	logFindingCmd.Flags().StringVar(&findingConf, "confidence", "", "high | medium | low — only honoured when lower than derived")
	logFindingCmd.Flags().StringVar(&findingTargetID, "target", "", "Task or project id (default: the bound task)")
	_ = logFindingCmd.MarkFlagRequired("finding")
	_ = logFindingCmd.MarkFlagRequired("source-kind")

	rootCmd.AddCommand(logDecisionCmd, logFindingCmd)
}
