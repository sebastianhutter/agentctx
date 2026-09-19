# Enforcement

What agentctx makes impossible, what it merely encourages, and what it
deliberately leaves alone.

## Why the tool schema and not instructions

Instructions are host-specific and best-effort. A `CLAUDE.md` is Claude Code's, an
`AGENTS.md` is a convention, a system prompt is the host's business — and all of
them are advice a model weighs against everything else in its context.

A tool schema is not advice. A required argument is read identically by every
host, and an error returned from a call is a fact the model has to deal with
rather than a suggestion it can deprioritise. So everything load-bearing here is
either a required field or a refusal.

This also decides where the *instructions* live: `open_task` returns the working
agreement as part of its output. Tool output lands in context in every host, in
the same words, at the moment the work is about to start.

## What is enforced

| Rule | Mechanism | Why it is worth a refusal |
|---|---|---|
| A finding names its source | `source_kind` required | A claim with no provenance cannot be re-verified and is worth less than no claim — it looks like knowledge |
| A decision gives a reason | `rationale` required | Without it a decision can only be second-guessed later, never re-evaluated |
| A repo-scoped finding names its repo | validation | Otherwise the next task in that repository never finds it |
| A task is not created out of thin air | seed required on creation | agentctx holds no tracker credentials; the agent has to go look, and the task's first recorded fact is where it came from |
| A project is never invented | `open_project` required first | An auto-created project silently splits one corpus in two |
| A session does not close empty | `close_task` refuses | A session that learned nothing worth recording is usually one that forgot to record it |
| Confidence is not self-assessed | derived from `source_kind` | A model asked how sure it is says "very" |
| The task is named on every write | `task_id` required by the schema | It used to be inferred. A wrong guess files work under the wrong task and nobody notices for weeks — and unlike everything else here, it cannot be found later or undone |

Every refusal says what to do next. A refusal that only blocks is an obstacle;
one that names the missing argument is a correction.

## What is encouraged, not enforced

- **Logging at decision points rather than at the end.** Nothing can compel this —
  the verbs simply exist at the moment a decision is made, and the working
  agreement says so plainly.
- **Choosing the wider scope.** A model can always pick `task`. The tool
  description and the agreement both say task scope is the weak one; the schema
  cannot know which is right.
- **Writing findings someone else could use.** Style, not structure.

## What is deliberately not enforced

- **Blocking edits until `open_task` was called.** Tempting as a `PreToolUse`
  hook, but it turns the system into a nag before there is evidence it is needed,
  and it only works on one host. Keep it as the escalation if the numbers show
  sessions opening tasks and writing nothing.
- **Deduplicating findings.** Whether a near-duplicate should be refused or
  supersede its predecessor needs real duplicates to decide. The log stays
  append-only either way.
- **Anything that depends on a hook.** Hooks exist on one host. They are a
  convenience layer for the strongest host, never the mechanism — otherwise the
  system works on one host and silently does nothing on the others.

## Measuring whether it works

Every entry carries `host`. Inside a task runner that value is detected from the
environment rather than configured, because a runner and a plain session share one
user-level MCP registration and a hand-set variable cannot tell them apart.

Over the shared SSE server the environment is the menu bar app's, so the name the
MCP client announced at `initialize` is used instead. Without that substitution
every entry written through the shared endpoint would carry one identical wrong
host, and this measurement could not be made at all.

After a couple of weeks the log answers the only question that matters: which
hosts produced entries, and which opened tasks and wrote nothing. If the entries
are never read back, the write side was never the problem.
