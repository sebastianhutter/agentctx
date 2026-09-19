package config

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// The example is the only documentation of what can be configured, and it drifts
// silently: adding a field to the struct breaks nothing, so nothing says the
// example no longer mentions it. This walks the struct and insists.
func TestExampleConfigCoversEveryField(t *testing.T) {
	data, err := os.ReadFile("../../configs/config.example.json")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("the example config is not valid JSON: %v", err)
	}

	var walk func(rt reflect.Type, present map[string]any, path string)
	walk = func(rt reflect.Type, present map[string]any, path string) {
		for i := range rt.NumField() {
			field := rt.Field(i)
			tag := field.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")

			value, ok := present[name]
			if !ok {
				t.Errorf("configs/config.example.json is missing %q — add it, so the example "+
					"keeps describing everything that can be set", path+name)
				continue
			}
			if field.Type.Kind() == reflect.Struct {
				nested, ok := value.(map[string]any)
				if !ok {
					t.Errorf("%q should be an object in the example", path+name)
					continue
				}
				walk(field.Type, nested, path+name+".")
			}
		}
	}
	walk(reflect.TypeOf(Config{}), raw, "")
}

// The example must also load, or it documents something that does not work.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load("../../configs/config.example.json")
	if err != nil {
		t.Fatalf("the example config does not load: %v", err)
	}
	if cfg.GUI.MCPPort == cfg.GUI.WebPort {
		t.Error("the example gives the MCP and web servers the same port")
	}
}

// A config written before a field existed keeps that field's default, including
// inside a nested object that is itself present. Without this, adding a setting
// would silently zero it for everyone who already had a config file — web_port
// 0, auto_start_web false, and a menu item that does nothing.
func TestOlderConfigKeepsNewDefaults(t *testing.T) {
	path := t.TempDir() + "/config.json"
	older := `{
	  "vault_path": "/tmp/vault",
	  "gui": {"auto_start_mcp": true, "mcp_port": 31124, "start_on_login": true}
	}`
	if err := os.WriteFile(path, []byte(older), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GUI.WebPort != 31125 {
		t.Errorf("gui.web_port = %d, want the default 31125", cfg.GUI.WebPort)
	}
	if !cfg.GUI.AutoStartWeb {
		t.Error("gui.auto_start_web was zeroed rather than defaulted")
	}
	// And a field that *is* present must survive.
	if !cfg.GUI.StartOnLogin {
		t.Error("gui.start_on_login was overwritten by its default")
	}
}
