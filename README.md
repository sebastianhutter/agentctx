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

## Quick start

```bash
make build                                    # binary at bin/agentctx

cat > ~/.agentctx/config.json <<'JSON'
{ "vault_path": "~/Obsidian/agentctx", "state_dir": "~/.agentctx" }
JSON

bin/agentctx doctor                           # check config, host and task resolution

bin/agentctx open-project proj --name "Example project" \
    --jira-project PROJ --repo acme/infrastructure

bin/agentctx open-task proj-142 --title "Rework the network module" \
    --key PROJ-142 --url https://example.atlassian.net/browse/PROJ-142 \
    --repo acme/infrastructure
```

## The five verbs

| Verb | What it does |
|---|---|
| `open_project` | What do we know about this project? Creates the project note on first call |
| `open_task` | What do we know about this task, its project and its repos? Creates the task note from a seed |
| `log_decision` | A choice and why it was made. Rationale required |
| `log_finding` | Something looked up and confirmed. Source required, scope chosen deliberately |
| `close_task` | Closes with a summary — and refuses if nothing was logged |

Each is both an MCP tool and a CLI subcommand over the same core, so there is one
write path and no drift between what an agent sees and what you see.

## Register as an MCP server

```json
{
  "mcpServers": {
    "agentctx": { "command": "/path/to/agentctx", "args": ["serve"] }
  }
}
```

Registering at user level (`~/.claude/`) makes it reachable from a fresh worktree,
including one created by a task runner.

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
- **Never infer the task from the working directory.** Resolution goes: explicit
  argument, then a binding recorded by an earlier call, then a tracker key in the
  branch name, then refuse. A branch is often named after a task title and carries
  no key at all.

## Documentation

- [`docs/implementation-plan.md`](docs/implementation-plan.md) — design, decisions and build order
- [`docs/vault-schema.md`](docs/vault-schema.md) — frontmatter contracts and entry shape
- [`docs/enforcement.md`](docs/enforcement.md) — why required fields and refusals, and what is deliberately not enforced
- [`CLAUDE.md`](CLAUDE.md) — working rules for this repository

## Licence

MIT
