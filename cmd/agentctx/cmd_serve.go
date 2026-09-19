package main

import (
	"github.com/spf13/cobra"

	localMCP "github.com/sebastianhutter/agentctx/internal/mcp"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server on stdin/stdout",
	Long: `Start the MCP server over stdio.

Every host spawns its own process. File-per-entry means there is nothing to
coordinate between them, so a long-lived daemon would buy locking and caching
that a single person's vault does not need.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return localMCP.ServeStdio(configPath, version)
	},
}

func init() { rootCmd.AddCommand(serveCmd) }
