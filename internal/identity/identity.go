// Package identity works out which task an agent is talking about, and which
// host is doing the talking.
//
// The one rule: never infer the task from the working directory. A worktree path
// is made from a task *title* and a hash, and a plausible-looking guess files a
// decision under the wrong task silently — which is worse than refusing, because
// nobody finds out for weeks.
//
// What the directory is allowed to do is act as a *key* into a binding that was
// established explicitly. That is the difference between remembering an answer
// and inventing one.
package identity

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Hosts. The distinction that matters is the middle one: Claude Code inside
// Kepler and Claude Code on its own share a single ~/.claude registration, so an
// environment variable set by hand cannot tell them apart. Kepler exports its
// own, which makes the split free and the drift measurement possible.
const (
	HostKeplerClaudeCode = "kepler-claude-code"
	HostClaudeCode       = "claude-code"
	HostUnknown          = "unknown"
)

// Kepler's environment, inherited by any MCP server it spawns.
const (
	envKeplerTask    = "KEPLER_TASK_ID"
	envKeplerSession = "KEPLER_SESSION_ID"
	envAgentHost     = "AGENT_HOST"
	envClaudeCode    = "CLAUDECODE"
	envClaudeSession = "CLAUDE_CODE_SESSION_ID"
)

// ErrUnresolved means no binding, argument or branch name identified a task.
var ErrUnresolved = errors.New("could not determine which task this is")

// Env is the process environment, injected so tests need not mutate the real one.
type Env struct {
	Getenv func(string) string
	Getwd  func() (string, error)
}

// OSEnv returns an Env backed by the real process.
func OSEnv() Env {
	return Env{Getenv: os.Getenv, Getwd: os.Getwd}
}

// Host returns the host name to stamp on entries.
//
// Order: explicit configuration, then AGENT_HOST, then detection. Configuration
// wins because Claude Desktop cannot set an environment variable per server and
// has to say so in the config file.
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

// KeplerTaskID returns the Kepler task uuid, or "" outside Kepler.
//
// Correlation only. It identifies a Kepler task, not an agentctx task, and the
// two are different things: one Kepler task can span several issues.
func (e Env) KeplerTaskID() string { return e.Getenv(envKeplerTask) }

// Bindings maps a host-side identifier to an agentctx task id.
//
// A cache, not a record: it holds no knowledge, lives outside the vault, and
// deleting it costs one explicit argument on the next call. Two keys per task —
// the Kepler task uuid and the worktree path — because a Kepler task can hold
// several worktrees and a plain Claude Code session has no uuid at all.
type Bindings struct {
	path    string
	Entries map[string]string `json:"entries"`
}

// LoadBindings reads the bindings file, returning an empty set if absent.
func LoadBindings(stateDir string) (*Bindings, error) {
	b := &Bindings{path: filepath.Join(stateDir, "bindings.json"), Entries: map[string]string{}}

	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return b, nil
		}
		return nil, fmt.Errorf("read bindings: %w", err)
	}
	if err := json.Unmarshal(data, b); err != nil {
		// A corrupt cache is not worth an error: drop it and rebuild.
		slog.Warn("bindings file unreadable, starting empty", "path", b.path, "err", err)
		b.Entries = map[string]string{}
	}
	if b.Entries == nil {
		b.Entries = map[string]string{}
	}
	return b, nil
}

// Save writes the bindings file atomically.
//
// Concurrent writers are last-write-wins, which is acceptable precisely because
// this is a cache: the loser's binding is re-established by its next explicit
// call.
func (b *Bindings) Save() error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal bindings: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".bindings-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp bindings: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write bindings: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close bindings: %w", err)
	}
	return os.Rename(tmpName, b.path)
}

// Bind records that a host-side identifier refers to a task.
func (b *Bindings) Bind(key, taskID string) {
	if key == "" || taskID == "" {
		return
	}
	b.Entries[key] = taskID
}

// Lookup returns the task bound to a key.
func (b *Bindings) Lookup(key string) string { return b.Entries[key] }

// KeplerKey and PathKey namespace the two kinds of identifier.
func KeplerKey(uuid string) string { return "kepler:" + uuid }

// PathKey namespaces a worktree path.
func PathKey(path string) string { return "path:" + path }

// Resolution records which task was resolved and how, so a caller can say so.
// "Resolved from the branch name" deserves less trust than "you told me", and
// the difference belongs in the output rather than in a guess.
type Resolution struct {
	TaskID string
	Via    string
}

// issueKey matches a tracker key inside a branch name.
var issueKey = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9]{1,9})-([0-9]+)\b`)

// Resolver resolves task ids.
type Resolver struct {
	Env      Env
	Bindings *Bindings

	// Git runs a git command in a directory. Injected for tests.
	Git func(dir string, args ...string) (string, error)
}

// NewResolver returns a Resolver using the real git binary.
func NewResolver(env Env, bindings *Bindings) *Resolver {
	return &Resolver{Env: env, Bindings: bindings, Git: runGit}
}

// Resolve determines the task id.
//
// Order, first hit wins:
//
//  1. the explicit argument — always right, because someone said it
//  2. a binding on the Kepler task uuid
//  3. a binding on the worktree path
//  4. a tracker key parsed out of the branch name
//  5. refuse, and say what to pass
//
// Step 4 is last on purpose. Kepler names a branch after the task *title* unless
// the task was created from an issue, so a key is often simply absent — and when
// the parse does fire it is a guess, which is why the Resolution says so.
func (r *Resolver) Resolve(explicit string) (Resolution, error) {
	if id := strings.TrimSpace(explicit); id != "" {
		return Resolution{TaskID: id, Via: "argument"}, nil
	}

	if uuid := r.Env.KeplerTaskID(); uuid != "" {
		if id := r.Bindings.Lookup(KeplerKey(uuid)); id != "" {
			return Resolution{TaskID: id, Via: "binding on the Kepler task"}, nil
		}
	}

	root := r.WorktreeRoot()
	if root != "" {
		if id := r.Bindings.Lookup(PathKey(root)); id != "" {
			return Resolution{TaskID: id, Via: "binding on the worktree"}, nil
		}
	}

	if root != "" {
		if branch, err := r.Git(root, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			if m := issueKey.FindStringSubmatch(branch); m != nil {
				id := strings.ToLower(m[1] + "-" + m[2])
				return Resolution{TaskID: id, Via: "key parsed from the branch name (a guess — pass task_id if wrong)"}, nil
			}
		}
	}

	return Resolution{}, fmt.Errorf("%w: pass task_id explicitly (the branch name carries no tracker key, "+
		"and nothing is bound to this worktree yet)", ErrUnresolved)
}

// RememberTask binds the current host context to a task, so later calls in the
// same session need no argument.
func (r *Resolver) RememberTask(taskID string) error {
	if uuid := r.Env.KeplerTaskID(); uuid != "" {
		r.Bindings.Bind(KeplerKey(uuid), taskID)
	}
	if root := r.WorktreeRoot(); root != "" {
		r.Bindings.Bind(PathKey(root), taskID)
	}
	return r.Bindings.Save()
}

// WorktreeRoot returns the git worktree containing the working directory, or the
// working directory itself when it is not a checkout.
func (r *Resolver) WorktreeRoot() string {
	wd, err := r.Env.Getwd()
	if err != nil {
		return ""
	}
	if out, err := r.Git(wd, "rev-parse", "--show-toplevel"); err == nil && out != "" {
		return out
	}
	return wd
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
