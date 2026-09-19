package identity

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func testEnv(vars map[string]string, wd string) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		Getwd:  func() (string, error) { return wd, nil },
	}
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
			if got := testEnv(tc.vars, "/tmp").Host(tc.configured); got != tc.want {
				t.Errorf("Host = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSession(t *testing.T) {
	if got := testEnv(map[string]string{envKeplerSession: "k", envClaudeSession: "c"}, "/tmp").Session(); got != "k" {
		t.Errorf("Session = %q, want the Kepler one", got)
	}
	if got := testEnv(map[string]string{envClaudeSession: "c"}, "/tmp").Session(); got != "c" {
		t.Errorf("Session = %q, want the Claude Code one", got)
	}
}

func TestBindingsRoundTrip(t *testing.T) {
	dir := t.TempDir()

	b, err := LoadBindings(dir)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	b.Bind(KeplerKey("uuid-1"), "proj-142")
	b.Bind(PathKey("/w/one"), "proj-142")
	if err := b.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	again, err := LoadBindings(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Lookup(KeplerKey("uuid-1")) != "proj-142" || again.Lookup(PathKey("/w/one")) != "proj-142" {
		t.Errorf("bindings did not survive a round trip: %+v", again.Entries)
	}
	if again.Lookup("absent") != "" {
		t.Error("Lookup invented a binding")
	}
}

// The cache must never be load-bearing: a corrupt file costs an explicit
// argument, not an error.
func TestLoadBindingsToleratesCorruption(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/bindings.json", []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := LoadBindings(dir)
	if err != nil {
		t.Fatalf("LoadBindings: %v", err)
	}
	if len(b.Entries) != 0 {
		t.Errorf("entries = %v, want empty", b.Entries)
	}
}

func newResolver(vars map[string]string, wd string, git func(string, ...string) (string, error)) *Resolver {
	b := &Bindings{path: wd + "/bindings.json", Entries: map[string]string{}}
	return &Resolver{Env: testEnv(vars, wd), Bindings: b, Git: git}
}

func gitReturning(values map[string]string) func(string, ...string) (string, error) {
	return func(_ string, args ...string) (string, error) {
		key := strings.Join(args, " ")
		if v, ok := values[key]; ok {
			return v, nil
		}
		return "", fmt.Errorf("git %s: not available", key)
	}
}

func TestResolveOrder(t *testing.T) {
	git := gitReturning(map[string]string{
		"rev-parse --show-toplevel":   "/w/repo",
		"rev-parse --abbrev-ref HEAD": "feature/PROJ-142-do-a-thing",
	})

	t.Run("explicit argument wins", func(t *testing.T) {
		r := newResolver(map[string]string{envKeplerTask: "uuid"}, "/w/repo", git)
		r.Bindings.Bind(KeplerKey("uuid"), "bound-task")
		got, err := r.Resolve("explicit-task")
		if err != nil || got.TaskID != "explicit-task" || got.Via != "argument" {
			t.Errorf("Resolve = %+v, %v", got, err)
		}
	})

	t.Run("kepler binding before branch", func(t *testing.T) {
		r := newResolver(map[string]string{envKeplerTask: "uuid"}, "/w/repo", git)
		r.Bindings.Bind(KeplerKey("uuid"), "bound-task")
		got, err := r.Resolve("")
		if err != nil || got.TaskID != "bound-task" {
			t.Errorf("Resolve = %+v, %v", got, err)
		}
	})

	t.Run("worktree binding", func(t *testing.T) {
		r := newResolver(map[string]string{}, "/w/repo", git)
		r.Bindings.Bind(PathKey("/w/repo"), "path-task")
		got, err := r.Resolve("")
		if err != nil || got.TaskID != "path-task" {
			t.Errorf("Resolve = %+v, %v", got, err)
		}
	})

	t.Run("branch parse is last and says it guessed", func(t *testing.T) {
		r := newResolver(map[string]string{}, "/w/repo", git)
		got, err := r.Resolve("")
		if err != nil || got.TaskID != "proj-142" {
			t.Fatalf("Resolve = %+v, %v", got, err)
		}
		if !strings.Contains(got.Via, "guess") {
			t.Errorf("Via = %q, want it to admit the guess", got.Via)
		}
	})
}

// The Kepler case the notes got wrong: a branch named after the task title
// carries no key, so resolution must refuse rather than invent one.
func TestResolveRefusesWhenBranchHasNoKey(t *testing.T) {
	git := gitReturning(map[string]string{
		"rev-parse --show-toplevel":   "/w/repo",
		"rev-parse --abbrev-ref HEAD": "please-review-the-notes-in",
	})
	r := newResolver(map[string]string{envKeplerTask: "uuid"}, "/w/repo", git)

	_, err := r.Resolve("")
	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("Resolve returned %v, want ErrUnresolved", err)
	}
	if !strings.Contains(err.Error(), "task_id") {
		t.Errorf("error does not say what to pass: %v", err)
	}
}

func TestResolveOutsideGitRepository(t *testing.T) {
	git := func(string, ...string) (string, error) { return "", errors.New("not a repository") }
	r := newResolver(map[string]string{}, "/tmp/plain", git)

	if _, err := r.Resolve(""); !errors.Is(err, ErrUnresolved) {
		t.Errorf("Resolve returned %v, want ErrUnresolved", err)
	}
	// Falls back to the working directory rather than giving up on a key.
	if got := r.WorktreeRoot(); got != "/tmp/plain" {
		t.Errorf("WorktreeRoot = %q", got)
	}
}

func TestRememberTaskBindsBothKeys(t *testing.T) {
	dir := t.TempDir()
	git := gitReturning(map[string]string{"rev-parse --show-toplevel": dir})
	r := &Resolver{
		Env:      testEnv(map[string]string{envKeplerTask: "uuid"}, dir),
		Bindings: &Bindings{path: dir + "/bindings.json", Entries: map[string]string{}},
		Git:      git,
	}

	if err := r.RememberTask("proj-142"); err != nil {
		t.Fatalf("RememberTask: %v", err)
	}
	if r.Bindings.Lookup(KeplerKey("uuid")) != "proj-142" {
		t.Error("Kepler key not bound")
	}
	if r.Bindings.Lookup(PathKey(dir)) != "proj-142" {
		t.Error("worktree path not bound")
	}

	// And a later call with no argument finds it.
	got, err := r.Resolve("")
	if err != nil || got.TaskID != "proj-142" {
		t.Errorf("Resolve after RememberTask = %+v, %v", got, err)
	}
}
