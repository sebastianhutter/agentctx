package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/identity"
	"github.com/sebastianhutter/agentctx/internal/store"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Show resolved configuration, host detection and task resolution",
	Long: `Print what agentctx has worked out about its own situation: where the vault
is, which host it thinks it is running on, how to register it, and what the
vault already holds.

Which task an entry belongs to is never worked out here — it is an argument on
every verb that writes.`,
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
		fmt.Println("  (stamped on entries written through a stdio server or this CLI;")
		fmt.Println("   a shared SSE server uses the connected client's own name instead)")

		printRegistration()

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

// printRegistration shows how to register this exact binary.
//
// It resolves its own path rather than printing a placeholder, because the
// answer differs by install method: inside an .app bundle the binary lives at a
// path nobody would guess, and this command is what the bundle runs when it is
// double-clicked.
func printRegistration() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}

	fmt.Println("\nRegistration")
	fmt.Printf("  binary:     %s\n", exe)
	fmt.Println("\n  Register as an MCP server (e.g. in ~/.claude/settings.json or")
	fmt.Println("  Claude Desktop's config), so every host spawns its own process:")
	fmt.Printf(`
    {
      "mcpServers": {
        "agentctx": { "command": %q, "args": ["serve"] }
      }
    }
`, exe)

	if onPath, err := exec.LookPath("agentctx"); err != nil || onPath != exe {
		fmt.Println("\n  For the command line, put it on your PATH:")
		fmt.Printf("    mkdir -p ~/.local/bin && ln -sf %q ~/.local/bin/agentctx\n", exe)
	}
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func init() { rootCmd.AddCommand(doctorCmd) }
