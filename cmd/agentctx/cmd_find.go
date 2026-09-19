package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	findRepo          string
	findIncludeClosed bool
	findLimit         int
)

var findCmd = &cobra.Command{
	Use:   "find [words...]",
	Short: "Find which project or task some work belongs to",
	Long: `Find places to look: project and task ids, with why each matched.

Returns identifiers rather than content — open one to see what is recorded.
With no arguments it lists everything.

Matching is literal and case-insensitive, over ids, titles, repositories,
tracker keys and the text of anything logged, so a query describing a problem
can surface the task where it was solved. It is not a semantic search.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}

		req := service.FindRequest{
			Query:         strings.Join(args, " "),
			Repo:          findRepo,
			IncludeClosed: findIncludeClosed,
			Limit:         findLimit,
		}

		matches, err := svc.Find(req)
		if err != nil {
			return err
		}
		fmt.Print(service.RenderMatches(matches, req))
		return nil
	},
}

func init() {
	findCmd.Flags().StringVar(&findRepo, "repo", "", "Restrict to a repository")
	findCmd.Flags().BoolVar(&findIncludeClosed, "include-closed", false, "Include closed tasks")
	findCmd.Flags().IntVar(&findLimit, "limit", 0, "Maximum results (default 20)")

	rootCmd.AddCommand(findCmd)
}
