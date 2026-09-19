package gui

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/server"

	localMCP "github.com/sebastianhutter/agentctx/internal/mcp"
)

// MCPService manages the in-process MCP SSE server behind the menu.
type MCPService struct {
	mu        sync.Mutex
	running   bool
	port      int
	sseServer *server.SSEServer

	// configPath and version are passed to each server it creates.
	configPath string
	version    string
}

// Start begins serving SSE on the given port.
func (s *MCPService) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("MCP server already running on port %d", s.port)
	}

	// Bind once to find out whether the port is free, and report it as a clear
	// error rather than letting the goroutine below fail into a log nobody
	// reads. A menu that says "Running" over a server that never started is
	// worse than one that says why it did not.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("port %d unavailable: %w", port, err)
	}
	ln.Close()

	sseServer := localMCP.NewSSEServer(s.configPath, s.version)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	slog.Info("starting MCP server (SSE)", "addr", addr, "url", s.urlFor(port))

	s.sseServer = sseServer
	s.port = port
	s.running = true

	go func() {
		if err := sseServer.Start(addr); err != nil {
			slog.Error("MCP SSE server stopped", "err", err)
			s.mu.Lock()
			s.running = false
			s.mu.Unlock()
		}
	}()

	return nil
}

// Stop shuts the server down, waiting briefly for connections to close.
func (s *MCPService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}

	slog.Info("stopping MCP server")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if s.sseServer != nil {
		if err := s.sseServer.Shutdown(ctx); err != nil {
			slog.Error("MCP shutdown error", "err", err)
		}
		s.sseServer = nil
	}

	s.running = false
	slog.Info("MCP server stopped")
}

// Restart stops and starts the server, which is how a configuration change
// reaches it: the tools reload config per call, but the listening port does not.
func (s *MCPService) Restart(port int) error {
	s.Stop()
	return s.Start(port)
}

// IsRunning reports whether the server is up.
func (s *MCPService) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Port returns the port in use.
func (s *MCPService) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// URL returns the endpoint a client registers, or "" when stopped.
func (s *MCPService) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return ""
	}
	return s.urlFor(s.port)
}

func (s *MCPService) urlFor(port int) string {
	return fmt.Sprintf("http://localhost:%d/sse", port)
}
