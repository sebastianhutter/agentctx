// Package identity works out which host is writing, so an entry records where
// it came from.
//
// It does not work out *which task* is meant. That is an argument, required by
// every verb that writes. agentctx used to infer it from a recorded binding, a
// worktree path or a branch name, and the inference is gone on purpose: it
// saved an agent from repeating a string it already had in front of it, and
// paid for that with the one failure nothing else here can detect — an entry
// filed against the wrong task looks exactly like an entry filed correctly, and
// nobody finds out for weeks. A required parameter has no such failure mode.
package identity

import (
	"os"
	"strings"
)

// Hosts. The distinction that matters is the middle one: Claude Code inside
// Kepler and Claude Code on its own share a single ~/.claude registration, so an
// environment variable set by hand cannot tell them apart. Kepler exports its
// own, which makes the split free.
const (
	HostKeplerClaudeCode = "kepler-claude-code"
	HostClaudeCode       = "claude-code"
	HostUnknown          = "unknown"
)

const (
	envKeplerTask    = "KEPLER_TASK_ID"
	envKeplerSession = "KEPLER_SESSION_ID"
	envAgentHost     = "AGENT_HOST"
	envClaudeCode    = "CLAUDECODE"
	envClaudeSession = "CLAUDE_CODE_SESSION_ID"
)

// Env is the process environment, injected so tests need not mutate the real one.
type Env struct {
	Getenv func(string) string
}

// OSEnv returns an Env backed by the real process.
func OSEnv() Env {
	return Env{Getenv: os.Getenv}
}

// Host returns the host name to stamp on entries.
//
// Order: explicit configuration, then AGENT_HOST, then detection. Configuration
// wins because Claude Desktop cannot set an environment variable per server and
// has to say so in the config file.
//
// Only meaningful when this process was spawned by the agent — over a shared
// server the environment is the server's, and the caller's own name is used
// instead. See service.Transport.
func (e Env) Host(configured string) string {
	if h := strings.TrimSpace(configured); h != "" {
		return h
	}
	if h := strings.TrimSpace(e.Getenv(envAgentHost)); h != "" {
		return h
	}
	if e.Getenv(envKeplerTask) != "" {
		return HostKeplerClaudeCode
	}
	if e.Getenv(envClaudeCode) != "" {
		return HostClaudeCode
	}
	return HostUnknown
}

// Session returns the host's session id, if it exposes one.
func (e Env) Session() string {
	if s := e.Getenv(envKeplerSession); s != "" {
		return s
	}
	return e.Getenv(envClaudeSession)
}
