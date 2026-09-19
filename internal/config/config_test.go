package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Stale.MaxAgeDays != 30 {
		t.Errorf("stale.max_age_days = %d, want 30", cfg.Stale.MaxAgeDays)
	}
	if !cfg.Confidence.DeriveFromSource {
		t.Error("confidence.derive_from_source = false, want true by default")
	}
	// Not 31123: that is local-rag's port, and two menu bar apps fighting over
	// one port fail by silently not starting.
	if cfg.GUI.MCPPort != 31124 {
		t.Errorf("gui.mcp_port = %d, want 31124", cfg.GUI.MCPPort)
	}
	if !cfg.GUI.AutoStartMCP {
		t.Error("gui.auto_start_mcp = false, want true by default")
	}
}

func TestLoadKeepsDefaultsForAbsentFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, `{"vault_path": "/tmp/vault"}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.VaultPath != "/tmp/vault" {
		t.Errorf("vault_path = %q", cfg.VaultPath)
	}
	if cfg.Stale.MaxAgeDays != 30 {
		t.Errorf("a partial config dropped a default: max_age_days = %d", cfg.Stale.MaxAgeDays)
	}
}

// A malformed config must fail loudly. Silently falling back to defaults would
// point agentctx at no vault at all and look like a first run.
func TestLoadRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, `{"vault_path": `)

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted malformed JSON")
	}
}

func TestExpandAndUnexpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := expandPath("~/x"), filepath.Join(home, "x"); got != want {
		t.Errorf("expandPath(~/x) = %q, want %q", got, want)
	}
	if got := UnexpandPath(filepath.Join(home, "x")); got != "~/x" {
		t.Errorf("UnexpandPath = %q, want ~/x", got)
	}
	if got := UnexpandPath("/etc/x"); got != "/etc/x" {
		t.Errorf("UnexpandPath(/etc/x) = %q, want unchanged", got)
	}
}

func TestValidate(t *testing.T) {
	vault := t.TempDir()

	t.Run("clean", func(t *testing.T) {
		cfg := &Config{VaultPath: vault, StateDir: t.TempDir(), Stale: StaleConfig{MaxAgeDays: 30},
			GUI: GUIConfig{MCPPort: 31124}}
		if errs := cfg.Validate(); len(errs) != 0 {
			t.Errorf("Validate returned %v, want none", errs)
		}
	})

	t.Run("missing vault", func(t *testing.T) {
		cfg := &Config{VaultPath: filepath.Join(vault, "nope"), StateDir: t.TempDir(), GUI: GUIConfig{MCPPort: 31124}}
		if errs := cfg.Validate(); len(errs) != 1 {
			t.Errorf("Validate returned %v, want one error", errs)
		}
	})

	t.Run("empty vault path", func(t *testing.T) {
		cfg := &Config{StateDir: t.TempDir(), GUI: GUIConfig{MCPPort: 31124}}
		errs := cfg.Validate()
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "vault_path is empty") {
			t.Errorf("Validate returned %v, want an empty-vault_path error", errs)
		}
	})

	// State inside the vault would be synced, and bindings are machine-local.
	t.Run("state dir inside vault", func(t *testing.T) {
		cfg := &Config{VaultPath: vault, StateDir: filepath.Join(vault, "state"), GUI: GUIConfig{MCPPort: 31124}}
		errs := cfg.Validate()
		if len(errs) != 1 || !strings.Contains(errs[0].Error(), "must not be synced") {
			t.Errorf("Validate returned %v, want a synced-state error", errs)
		}
	})
}

func TestSaveRoundTripsAndPreservesUnknownKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	write(t, path, `{"vault_path": "/tmp/v", "future_key": "keep me"}`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Stale.MaxAgeDays = 7
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "future_key") {
		t.Error("Save dropped an unknown key")
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Stale.MaxAgeDays != 7 {
		t.Errorf("max_age_days did not round-trip: %d", reloaded.Stale.MaxAgeDays)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
