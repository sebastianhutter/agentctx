package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var relinkDryRun bool

var relinkCmd = &cobra.Command{
	Use:   "relink",
	Short: "Add parent and repo_note links to files written before agentctx wrote them",
	Long: `Add link properties to existing files, so the vault's graph connects an entry
to its task, a task to its project, and a repo-scoped entry to its repo note.

New files carry these links from the moment they are written. This backfills
the ones that do not, and rewrites bare [[id]] links in dependency entries to
path-qualified ones.

The one command that edits log entries. It only adds links derived from ids
already in each file; no fact changes, and fields added in Obsidian survive.
Safe to run again: a second run changes nothing, unless a repo note has
appeared since.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newService()
		if err != nil {
			return err
		}
		report, err := svc.Store.Relink(relinkDryRun)
		if err != nil {
			return err
		}
		for _, path := range report.Changed {
			fmt.Println(path)
		}
		verb := "Relinked"
		if relinkDryRun {
			verb = "Would relink"
		}
		fmt.Printf("%s %d of %d files\n", verb, len(report.Changed), report.Scanned)
		return nil
	},
}

func init() {
	relinkCmd.Flags().BoolVar(&relinkDryRun, "dry-run", false, "List what would change without writing")

	rootCmd.AddCommand(relinkCmd)
}
