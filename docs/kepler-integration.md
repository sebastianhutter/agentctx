# Running inside Kepler

Kepler creates an isolated git worktree per task, at a path that varies, and can
attach several repositories to one task. That makes identity the thing to get
right: an entry filed under the wrong task looks exactly like an entry filed
correctly.

Everything below was checked inside a live Kepler session rather than assumed.

## Kepler exports its identity to child processes

An MCP server spawned by Claude Code inside Kepler inherits:

```
KEPLER_TASK_ID=<uuid>          # stable per Kepler task, across all its sessions
KEPLER_SESSION_ID=<uuid>
KEPLER_REPO_ID=                # empty in a multi-repo task — do not rely on it
KEPLER_WORKTREE_ID=            # empty in a multi-repo task — do not rely on it
KEPLER_BREADCRUMB_DIR=…        # worktree→task store, keyed by a path hash
```

Two consequences.

**Task binding.** `KEPLER_TASK_ID` is stable across every session of a task, so
one explicit `open_task` binds it and every later call in any session of that
task resolves with no argument. It is a *correlation* key, not a task id: one
Kepler task can cover several issues, so it is recorded in the task note as
`kepler_task_id` and never used as the task's own identity.

**Host detection is free, and needed.** Claude Code inside Kepler and Claude Code
on its own share a single `~/.claude/` MCP registration, so a hand-set
`AGENT_HOST` stamps the same value for both — and "does Kepler produce log
entries?" becomes unmeasurable. The presence of `KEPLER_TASK_ID` is the
discriminator. `AGENT_HOST` remains as an override for hosts that cannot be
detected, such as Desktop.

`KEPLER_REPO_ID` and `KEPLER_WORKTREE_ID` came back empty in a multi-repo task.
For `scope: repo` use the working directory of the call or an explicit `repo`
argument.

## The branch name usually carries no issue key

Kepler names the branch after the **task title** unless the task was created from
an issue. A real example: a task called "please review the notes in …" produces
the branch `please-review-the-notes-in`.

So the resolution chain puts the branch parse last, and when it does fire, the
bundle says the id was guessed rather than given. Guessing silently is worse than
refusing: the refusal costs one argument, the wrong guess costs weeks of entries
filed under the wrong task.

## `git config --worktree` is not usable here

The obvious place to record a task binding is the worktree's git config. It does
not work:

```
fatal: --worktree cannot be used with multiple working trees unless the config
extension worktreeConfig is enabled
```

Enabling it writes `extensions.worktreeConfig` into the **shared** `.git/config` —
agentctx mutating the user's repositories as a side effect of opening a task — and
it still cannot express one Kepler task holding two worktrees.

agentctx uses `~/.agentctx/bindings.json` instead, keyed by both the Kepler task
uuid and the absolute worktree path. No repo mutation, works outside git, works
for multi-repo tasks, and deleting it costs one explicit argument.

## Kepler already supplies the task's resources

Kepler generates `~/kepler/tasks/<KEPLER_TASK_ID>/CLAUDE.md` and an identical
`AGENTS.md` listing every worktree with its repo name and base branch, plus every
attached link. An agent can read that and pass the repos straight into
`open_task`'s seed — no tracker credentials involved, which is part of why
agentctx needs none.

Do not write the working agreement into that file: Kepler overwrites it whenever
the task's resources change. A committed per-repo `AGENTS.md` is a different file
and is unaffected.

## Division of labour with Kepler's own task notes

Kepler has task-scoped shared context (`add_task_note`) that is injected into
every session of a task automatically. For in-flight state that is cheaper than
anything agentctx offers — nothing has to call a tool to read it.

What it cannot do is outlive the task or be scoped to a repository or project,
and it is invisible outside Kepler. So:

| Want | Use |
|---|---|
| In-flight state for this task, this week | Kepler task note |
| A decision worth defending in three months | `log_decision` |
| Anything true about a repo or project | `log_finding` with `scope: repo` / `project` |

The working agreement returned by `open_task` says this in as many words. Without
it, both systems get half-written and neither gets read.

## Worktrees are disposable

`discard_worktree` deletes the worktree *and* its branch. Anything stored inside a
checkout dies with it — which is why the vault lives outside, and why
`bindings.json` holds no knowledge worth keeping.
