package identity

import "testing"

func testEnv(vars map[string]string) Env {
	return Env{Getenv: func(k string) string { return vars[k] }}
}

func TestHostDetection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		configured string
		vars       map[string]string
		want       string
	}{
		{"config wins", "desktop", map[string]string{envKeplerTask: "u", envAgentHost: "x"}, "desktop"},
		{"AGENT_HOST next", "", map[string]string{envAgentHost: "opencode", envKeplerTask: "u"}, "opencode"},
		// The case a hand-set variable cannot cover: both hosts share one
		// ~/.claude registration, so only Kepler's own variable separates them.
		{"kepler detected", "", map[string]string{envKeplerTask: "uuid", envClaudeCode: "1"}, HostKeplerClaudeCode},
		{"plain claude code", "", map[string]string{envClaudeCode: "1"}, HostClaudeCode},
		{"nothing", "", map[string]string{}, HostUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := testEnv(tc.vars).Host(tc.configured); got != tc.want {
				t.Errorf("Host = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSession(t *testing.T) {
	if got := testEnv(map[string]string{envKeplerSession: "k", envClaudeSession: "c"}).Session(); got != "k" {
		t.Errorf("Session = %q, want the Kepler one", got)
	}
	if got := testEnv(map[string]string{envClaudeSession: "c"}).Session(); got != "c" {
		t.Errorf("Session = %q, want the Claude Code one", got)
	}
	if got := testEnv(map[string]string{}).Session(); got != "" {
		t.Errorf("Session = %q, want empty", got)
	}
}
