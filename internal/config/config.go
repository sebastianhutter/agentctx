// Package config handles loading and saving ~/.agentctx/config.json.
//
// The shape is deliberately small. agentctx makes no network calls and holds no
// credentials, so there is nothing to configure beyond where the vault is, where
// throwaway state goes, and two thresholds.
package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

var (
	// DefaultConfigDir also holds bindings.json. It is
	// deliberately outside the vault: nothing here may be synced.
	DefaultConfigDir  = filepath.Join(homeDir(), ".agentctx")
	DefaultConfigPath = filepath.Join(DefaultConfigDir, "config.json")
)

// StaleConfig controls when a stored finding is returned marked re-verify.
//
// Age is the only signal available: agentctx makes no network calls, so it
// cannot ask a tracker whether a page changed, and checking a file finding
// against HEAD would need a repository path it has no business knowing. A
// finding carries the commit it was taken at, and the agent can check that in
// one command if the finding matters enough.
type StaleConfig struct {
	MaxAgeDays int `json:"max_age_days"`
}

// ConfidenceConfig controls how a finding's confidence is decided.
//
// Derived from source.kind by default, because a model asked to rate its own
// confidence rates it high. An explicit value may lower the derived one, never
// raise it: doubt is worth recording, certainty has to be earned by provenance.
type ConfidenceConfig struct {
	DeriveFromSource bool `json:"derive_from_source"`
}

// GUIConfig holds settings for the menu bar app.
//
// The MCP port defaults to 31124 rather than anything rounder because 31123 is
// local-rag's: two menu bar apps on one machine should not fight over a port,
// and the failure if they do is a server that silently does not start.
type GUIConfig struct {
	AutoStartMCP bool `json:"auto_start_mcp"`
	MCPPort      int  `json:"mcp_port"`
	AutoStartWeb bool `json:"auto_start_web"`
	WebPort      int  `json:"web_port"`
	StartOnLogin bool `json:"start_on_login"`
}

// Config holds all application configuration.
type Config struct {
	// VaultPath is the dedicated agentctx vault — not the main Obsidian vault.
	// Keeping them separate is a design decision, not a convenience: everything
	// under this path is written by an agent.
	VaultPath string `json:"vault_path"`

	// StateDir holds bindings.json. Throwaway, rebuildable, never synced.
	StateDir string `json:"state_dir"`

	// Host overrides host detection. Normally empty: inside Claude Code the host
	// is detected from the environment, and Desktop is where this is worth
	// setting by hand.
	Host string `json:"host"`

	Stale      StaleConfig      `json:"stale"`
	Confidence ConfidenceConfig `json:"confidence"`
	GUI        GUIConfig        `json:"gui"`
}

// ExpandedVaultPath returns vault_path with ~ expanded.
func (c *Config) ExpandedVaultPath() string { return expandPath(c.VaultPath) }

// ExpandedStateDir returns state_dir with ~ expanded.
func (c *Config) ExpandedStateDir() string { return expandPath(c.StateDir) }

// Validate reports configuration that would make the program misbehave rather
// than fail, which is the worse outcome for a tool an agent calls unattended.
func (c *Config) Validate() []error {
	var errs []error

	vault := c.ExpandedVaultPath()
	if strings.TrimSpace(c.VaultPath) == "" {
		errs = append(errs, fmt.Errorf("vault_path is empty: set it in %s", DefaultConfigPath))
	} else if info, err := os.Stat(vault); err != nil {
		errs = append(errs, fmt.Errorf("vault_path %q does not exist: %w", vault, err))
	} else if !info.IsDir() {
		errs = append(errs, fmt.Errorf("vault_path %q is not a directory", vault))
	}

	// State inside the vault would be synced, and bindings.json is machine-local
	// by definition — two machines would fight over it.
	if state := c.ExpandedStateDir(); vault != "" && isWithin(state, vault) {
		errs = append(errs, fmt.Errorf("state_dir %q is inside the vault; it must not be synced", state))
	}

	if c.Stale.MaxAgeDays < 0 {
		errs = append(errs, fmt.Errorf("stale.max_age_days is negative (%d)", c.Stale.MaxAgeDays))
	}

	if c.GUI.MCPPort < 1024 || c.GUI.MCPPort > 65535 {
		errs = append(errs, fmt.Errorf("gui.mcp_port %d is outside the usable range 1024-65535", c.GUI.MCPPort))
	}
	if c.GUI.WebPort < 1024 || c.GUI.WebPort > 65535 {
		errs = append(errs, fmt.Errorf("gui.web_port %d is outside the usable range 1024-65535", c.GUI.WebPort))
	}
	if c.GUI.WebPort == c.GUI.MCPPort {
		errs = append(errs, fmt.Errorf("gui.web_port and gui.mcp_port are both %d; one of them will not start", c.GUI.MCPPort))
	}

	return errs
}

// Load reads configuration from the given path (or the default) and returns a
// Config with defaults applied for any missing fields.
//
// A missing file is not an error: the defaults are usable everywhere except
// vault_path, which Validate reports.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath
	}

	if err := os.MkdirAll(DefaultConfigDir, 0o755); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}

	cfg := defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Info("no config file found, using defaults", "path", path)
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	cfg.VaultPath = expandPath(cfg.VaultPath)
	cfg.StateDir = expandPath(cfg.StateDir)

	slog.Info("loaded config", "path", path)
	return cfg, nil
}

// Save writes the configuration to the given path (or the default), preserving
// any unknown keys already in the file.
func Save(cfg *Config, path string) error {
	if path == "" {
		path = DefaultConfigPath
	}

	existing := make(map[string]any)
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &existing)
	}

	existing["vault_path"] = UnexpandPath(cfg.VaultPath)
	existing["state_dir"] = UnexpandPath(cfg.StateDir)
	existing["host"] = cfg.Host
	existing["stale"] = cfg.Stale
	existing["confidence"] = cfg.Confidence
	existing["gui"] = cfg.GUI

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	out, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	out = append(out, '\n')

	if err := os.WriteFile(path, out, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	slog.Info("saved config", "path", path)
	return nil
}

// defaults returns a Config with all default values applied.
func defaults() *Config {
	return &Config{
		VaultPath:  "",
		StateDir:   DefaultConfigDir,
		Host:       "",
		Stale:      StaleConfig{MaxAgeDays: 30},
		Confidence: ConfidenceConfig{DeriveFromSource: true},
		GUI: GUIConfig{
			AutoStartMCP: true,
			MCPPort:      31124,
			AutoStartWeb: true,
			WebPort:      31125,
			StartOnLogin: false,
		},
	}
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp"
	}
	return home
}

func expandPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	if p == "~" {
		return homeDir()
	}
	return p
}

// UnexpandPath converts an absolute path back to ~/… form if under the home
// directory, so a saved config does not hard-code a user name.
func UnexpandPath(p string) string {
	home := homeDir()
	rel, err := filepath.Rel(home, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return "~/" + rel
}

// isWithin reports whether path is inside dir.
func isWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
