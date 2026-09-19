package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/identity"
	"github.com/sebastianhutter/agentctx/internal/store"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Show resolved configuration, host detection and task resolution",
	Long: `Print what agentctx has worked out about its own situation: where the vault
is, which host it thinks it is running on, and which task it would attribute an
entry to right now.

Host and task resolution are the two things that fail silently in normal use —
an entry filed under the wrong task looks exactly like an entry filed correctly.
This command is how you check without writing one.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := configPath
		if path == "" {
			path = config.DefaultConfigPath
		}

		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}

		fmt.Println("Configuration")
		fmt.Printf("  file:       %s", path)
		if _, statErr := os.Stat(path); statErr != nil {
			fmt.Print("  (absent — using defaults)")
		}
		fmt.Println()
		fmt.Printf("  vault:      %s\n", orUnset(cfg.ExpandedVaultPath()))
		fmt.Printf("  state:      %s\n", cfg.ExpandedStateDir())
		fmt.Printf("  stale after %d days\n", cfg.Stale.MaxAgeDays)

		problems := cfg.Validate()
		if len(problems) > 0 {
			fmt.Println("\nProblems")
			for _, p := range problems {
				fmt.Printf("  ✗ %v\n", p)
			}
		}

		env := identity.OSEnv()
		fmt.Println("\nHost")
		fmt.Printf("  detected:   %s\n", env.Host(cfg.Host))
		fmt.Printf("  session:    %s\n", orUnset(env.Session()))
		if uuid := env.KeplerTaskID(); uuid != "" {
			fmt.Printf("  kepler:     %s\n", uuid)
		} else {
			fmt.Println("  kepler:     not running under Kepler")
		}

		bindings, err := identity.LoadBindings(cfg.ExpandedStateDir())
		if err != nil {
			return err
		}
		resolver := identity.NewResolver(env, bindings)

		fmt.Println("\nTask resolution")
		fmt.Printf("  worktree:   %s\n", orUnset(resolver.WorktreeRoot()))
		if resolution, err := resolver.Resolve(""); err == nil {
			fmt.Printf("  would use:  %s (%s)\n", resolution.TaskID, resolution.Via)
		} else {
			fmt.Printf("  would use:  nothing — %v\n", err)
		}
		fmt.Printf("  bindings:   %d recorded\n", len(bindings.Entries))

		if len(problems) > 0 {
			return errors.New("configuration is not usable yet")
		}

		s := store.New(cfg.ExpandedVaultPath())
		projects, err := s.ListProjects()
		if err != nil {
			return err
		}
		fmt.Println("\nVault")
		if len(projects) == 0 {
			fmt.Println("  no projects yet — create one with: agentctx open-project <id> --name … --jira-project …")
			return nil
		}
		for _, id := range projects {
			tasks, err := s.ListTasks(id)
			if err != nil {
				return err
			}
			entries, err := s.Entries(id, store.EntryFilter{})
			if err != nil {
				return err
			}
			fmt.Printf("  %-16s %d tasks, %d log entries\n", id, len(tasks), len(entries))
		}
		return nil
	},
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func init() { rootCmd.AddCommand(doctorCmd) }
