package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/web"
)

var webPort int

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Serve the web interface in the foreground",
	Long: `Serve the local web interface: projects, tasks, repository notes and a
filterable log view.

Bound to loopback and unauthenticated, so it answers only to localhost. The menu
bar app runs exactly this server; this command is for running it on its own.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(configPath)
		if err != nil {
			return err
		}
		port := webPort
		if port == 0 {
			port = cfg.GUI.WebPort
		}

		server, err := web.New(configPath, version)
		if err != nil {
			return err
		}
		if err := server.Start(port); err != nil {
			return err
		}
		fmt.Printf("agentctx web interface on http://localhost:%d/\n", port)

		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop

		server.Stop()
		return nil
	},
}

func init() {
	webCmd.Flags().IntVar(&webPort, "port", 0, "Port to serve on (default: gui.web_port)")
	rootCmd.AddCommand(webCmd)
}
