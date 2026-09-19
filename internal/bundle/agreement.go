package bundle

// WorkingAgreement is appended to every task bundle.
//
// It ships as tool output rather than as documentation because tool output is
// the one channel every host reads identically. A CLAUDE.md is Claude Code's, an
// AGENTS.md is a convention, a system prompt is the host's business — but the
// result of a tool call lands in context everywhere, in the same words, at the
// moment the agent is about to do the work.
//
// It is short on purpose. An agreement nobody finishes reading is an agreement
// nobody keeps.
const WorkingAgreement = `---

## Working agreement

**Log while you work, not at the end.** Call ` + "`log_decision`" + ` when you choose
between options, and ` + "`log_finding`" + ` when you look something up and confirm it.
Sessions get compacted, interrupted and abandoned; anything saved for the end is
usually lost.

**Every finding needs a source.** Where it came from and when you retrieved it.
A claim with no provenance cannot be re-verified later, so it is worth less than
no claim at all — it looks like knowledge.

**Choose the scope deliberately. This is the part that matters.**

- ` + "`scope: repo`" + ` — true about a repository, whoever works on it next.
  *"This module's for_each breaks on a provider upgrade."*
- ` + "`scope: project`" + ` — true about the project as a whole.
  *"There is no staging environment."*
- ` + "`scope: task`" + ` — true only of this piece of work.
  *"We picked option B here."*

Repo and project scope is what outlives the task and makes the next one cheaper.
If a finding would help someone who has never heard of this task, it is not
task-scoped. When in doubt, prefer the wider scope.

**Task-scoped notes are the weakest thing here.** For in-flight state inside a
Kepler session, Kepler's own task notes are cheaper and are injected
automatically. Log a task-scoped entry when it is a decision worth defending in
three months, not as a scratchpad.

**Closing.** ` + "`close_task`" + ` refuses if nothing was logged since the task was
opened. That is deliberate: a session that learned nothing worth recording is
usually a session that forgot to record it.

Findings above marked **re-verify** are old or were inferred. Check before
relying on them, and log what you find — a confirmation is itself a finding.
`
