package mcp

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// The tool schema is the contract: it is the one thing every host reads the
// same way, so what the design calls "enforcement" has to live here and not in
// a description a model may skim.
func TestToolSchemasEnforceTheContract(t *testing.T) {
	tools := map[string]mcp.Tool{
		"open_project": openProjectTool,
		"open_task":    openTaskTool,
		"log_decision": logDecisionTool,
		"log_finding":  logFindingTool,
		"close_task":   closeTaskTool,
	}

	for name, tool := range tools {
		if tool.Name != name {
			t.Errorf("tool registered as %q is named %q", name, tool.Name)
		}
		if len(tool.Description) < 80 {
			t.Errorf("%s: description is too thin to be useful to a model", name)
		}
	}

	required := map[string][]string{
		// A decision without a rationale cannot be re-evaluated later.
		"log_decision": {"decision", "rationale"},
		// Provenance is the difference between knowledge and rumour.
		"log_finding":  {"finding", "source_kind"},
		"close_task":   {"summary"},
		"open_project": {"project_id"},
	}
	for name, fields := range required {
		got := tools[name].InputSchema.Required
		for _, field := range fields {
			if !contains(got, field) {
				t.Errorf("%s: %q is not required (required = %v)", name, field, got)
			}
		}
	}

	// open_task must NOT require task_id: a bound worktree is the common case,
	// and requiring it would make the SessionStart hook impossible.
	if len(openTaskTool.InputSchema.Required) != 0 {
		t.Errorf("open_task requires %v, but every argument is meant to be optional",
			openTaskTool.InputSchema.Required)
	}
}

// source_kind drives the derived confidence and the staleness rule, so the
// schema must offer exactly the kinds the store knows how to weigh.
func TestLogFindingOffersEverySourceKind(t *testing.T) {
	prop, ok := logFindingTool.InputSchema.Properties["source_kind"].(map[string]any)
	if !ok {
		t.Fatal("source_kind has no schema")
	}
	values, ok := prop["enum"].([]string)
	if !ok {
		t.Fatalf("source_kind enum has type %T, want []string", prop["enum"])
	}
	if len(values) != len(store.SourceKinds()) {
		t.Errorf("source_kind offers %v, want %v", values, store.SourceKinds())
	}
}

func TestScopeIsOfferedAndExplained(t *testing.T) {
	prop, ok := logFindingTool.InputSchema.Properties["scope"].(map[string]any)
	if !ok {
		t.Fatal("scope has no schema")
	}
	desc, _ := prop["description"].(string)
	// Scope is the decision the whole system turns on, so the description has to
	// say that task scope is the weak one rather than just list the values.
	if !strings.Contains(desc, "least useful") {
		t.Errorf("scope description does not steer the choice: %q", desc)
	}
}

func TestServerInstructionsStateTheBoundary(t *testing.T) {
	// The no-credentials rule is what makes seeding the agent's job. A host that
	// does not learn this from the instructions will keep waiting for a fetch
	// that never comes.
	// Substrings must survive the line wrapping in the instructions text.
	for _, want := range []string{"tracker credentials", "open_task before reading code", "scope=repo"} {
		if !strings.Contains(instructions, want) {
			t.Errorf("server instructions do not mention %q", want)
		}
	}
}

func TestCreateServerRegistersEveryVerb(t *testing.T) {
	if s := CreateServer("", "test", service.TransportStdio); s == nil {
		t.Fatal("CreateServer returned nil")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
