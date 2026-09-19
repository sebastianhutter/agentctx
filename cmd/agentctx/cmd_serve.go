package main

import (
	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/gui"
	localMCP "github.com/sebastianhutter/agentctx/internal/mcp"
)

var servePort int

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP server (stdio, or SSE with --port)",
	Long: `Start the MCP server.

Over stdio, which is the default, each host spawns its own process. That process
inherits the agent's working directory and environment, so open_task can resolve
a task from a recorded binding or the branch name rather than being told each
time. Prefer it where a host supports it.

With --port, serves SSE at http://127.0.0.1:<port>/sse for any number of clients
to share. Convenient, and one registration for every host — but a shared server
sees no caller's working directory or environment, so task ids must be passed
explicitly. The menu bar app runs exactly this server.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if servePort > 0 {
			return localMCP.ServeSSE(configPath, version, servePort)
		}
		return localMCP.ServeStdio(configPath, version)
	},
}

var guiCmd = &cobra.Command{
	Use:   "gui",
	Short: "Start the menu bar app (the default with no arguments)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return gui.Run(configPath, version)
	},
}

func init() {
	serveCmd.Flags().IntVar(&servePort, "port", 0, "Serve SSE on this port instead of stdio")
	rootCmd.AddCommand(serveCmd, guiCmd)
}
