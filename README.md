# agentctx

Durable, agent-writable knowledge for work that spans repositories and trackers.

Agents log **decisions** and **findings** while they work, each with provenance,
into a dedicated Obsidian vault. Entries are append-only markdown files. Findings
are scoped `task`, `repo` or `project` — and the repo- and project-scoped ones are
the point: they outlive the task that discovered them, so the next piece of work
in the same repository starts with what the last one found out.

It is **not** a task tracker, an aggregator or an integration hub.

## Why

Most real work spans several repositories plus a wiki. Project memory solves the
single-repo case; agent task systems solve in-flight state. Neither keeps "we
tried X, it failed because Y" once the work merges, and nothing carries it into
the next task in the same project.

## What it does not do

**agentctx makes no network calls and holds no credentials.** No Jira client, no
GitLab client, no search integration. The agent calling in already has those
tools: it looks a fact up and passes it in through a verb that requires a source.
That keeps one credential store fewer, one failure mode fewer, and makes the tool
schema — the only thing every agent host reads identically — do the enforcing.

The honest consequence: the first task in a new project gets a nearly empty
bundle back. The system pays off from the second one onward.

## Install

macOS only. Download the DMG from the releases page, drag agentctx to
Applications, and open it from the context menu the first time — it is not
notarised, so a plain double-click is refused.

It lives in the menu bar: start and stop the MCP server, copy its endpoint,
open the vault, edit settings.

Building it yourself needs Go and Xcode's command line tools:

```bash
make build      # binary at bin/agentctx
make app        # bin/agentctx.app
make dmg        # bin/agentctx.dmg
```

## Quick start

```bash
cat > ~/.agentctx/config.json <<'JSON'
{ "vault_path": "~/Obsidian/agentctx" }
JSON

agentctx doctor                               # config, host, task resolution, registration

agentctx open-project proj --name "Example project" \
    --jira-project PROJ --repo acme/infrastructure

agentctx open-task proj-142 --title "Rework the network module" \
    --key PROJ-142 --url https://example.atlassian.net/browse/PROJ-142 \
    --repo acme/infrastructure
```

## The five verbs

| Verb | What it does |
|---|---|
| `find` | Which project or task is this work? Returns ids and why each matched — never their contents |
| `open_project` | What do we know about this project? Creates the project note on first call |
| `open_task` | What do we know about this task, its project and its repos? Creates the task note from a seed |
| `log_decision` | A choice and why it was made. Rationale required |
| `log_finding` | Something looked up and confirmed. Source required, scope chosen deliberately |
| `close_task` | Closes with a summary — and refuses if nothing was logged |

Each is both an MCP tool and a CLI subcommand over the same core, so there is one
write path and no drift between what an agent sees and what you see.

## Register as an MCP server

Two transports.

**stdio.** Each host spawns its own process, which inherits the agent's
environment — so the host recorded on an entry distinguishes a task runner from
a plain session.

```json
{
  "mcpServers": {
    "agentctx": {
      "command": "/Applications/agentctx.app/Contents/MacOS/agentctx",
      "args": ["serve"]
    }
  }
}
```

Registering at user level (`~/.claude/`) makes it reachable from a fresh
worktree, including one created by a task runner.

**SSE — one shared server, run by the menu bar app.** Register the endpoint
(*Copy Endpoint* in the menu puts it on the clipboard):

```
http://localhost:31124/sse
```

Convenient, and the only option for a host that cannot spawn a process. One
difference worth knowing: a shared server cannot see any caller's environment,
so the host recorded on an entry comes from the MCP client's own name rather
than from detection. Task ids are required either way.

## Vault layout

```
<vault>/
  proj/
    proj.md                                      project note, created deliberately
    notes/                                       consolidated per-repo knowledge
    tasks/proj-142.md                            created lazily by open_task
    log/2026-09-19T1432-proj-142-decision-a3f0.md   append-only, one file per entry
```

`log/` is the source of truth. Note bodies are consolidated views of it, rewritten
only by the consolidation pass — never by an agent.

## Design constraints

- **Append-only.** The vault sits on a file-sync service: last-write-wins, no
  locking, parallel sessions normal. Two writers must produce two files, never one
  corruption. Creation is atomic and refuses to clobber.
- **`open_task` never rewrites the task note.** It appends an `opened` entry, which
  records more (every open, with host and time) and keeps one writer per file.
- **Nothing enters without provenance.** Confidence is derived from the source kind
  rather than asserted; a caller may lower it, never raise it.
- **The task is named, never inferred.** `task_id` is required on every verb that
  writes. An earlier version guessed it from a binding, the working directory or
  the branch name; that could file an entry against the wrong task with nothing
  looking wrong, which is the only mistake here that is neither detectable nor
  repairable. The tool schema is the right place to say what a call needs.

## Documentation

- [`docs/implementation-plan.md`](docs/implementation-plan.md) — design, decisions and build order
- [`docs/vault-schema.md`](docs/vault-schema.md) — frontmatter contracts and entry shape
- [`docs/enforcement.md`](docs/enforcement.md) — why required fields and refusals, and what is deliberately not enforced
- [`CLAUDE.md`](CLAUDE.md) — working rules for this repository

## Licence

MIT
