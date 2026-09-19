// Command agentctx is the CLI and MCP server for an agentctx vault.
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/service"
)

var (
	verbose    bool
	configPath string
	version    = "dev"
)

var rootCmd = &cobra.Command{
	Use:     "agentctx",
	Version: version,
	Short:   "Durable, agent-writable knowledge for work that spans repositories",
	Long: `agentctx records what agents decide and find out while they work, into a
dedicated Obsidian vault, with provenance and a scope.

Findings scoped to a repository or a project outlive the task that discovered
them — that is the point. Run "agentctx serve" to expose the verbs over MCP.`,
	// main prints the error; cobra printing it too would double every message.
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		level := slog.LevelWarn
		if verbose {
			level = slog.LevelDebug
		}
		// stderr, always: stdout belongs to the MCP protocol when serving, and
		// to the bundle when a hook is capturing this command's output.
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	},
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable debug logging")
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "Path to config.json (default ~/.agentctx/config.json)")
}

// newService loads configuration and builds the service every command uses.
func newService() (*service.Service, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return service.New(cfg)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
