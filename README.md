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

It lives in the menu bar: start and stop the MCP server, copy its endpoint, open
the web interface, open the vault, edit settings.

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

agentctx doctor                               # config, host, registration, vault checks

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
| `log_dependency` | This task waits for those. Refuses loops and unknown ids |
| `consolidate` | Folds logged entries into a note body — the only verb that rewrites anything |
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

## Web interface

`http://localhost:31125/` — projects, tasks, repository notes, a graph, the
vault checks, and a log view filterable by project, task, kind, scope, host and
repo. Opened from the menu bar, or run on its own with `agentctx web`.

- **Graph** (`/graph`) draws projects, tasks, repositories and, optionally, log
  entries as one picture. A repository is one node however many projects work on
  it, so the repos that tie projects together are visible — Obsidian's graph has
  a note per project for the same repo and cannot show that. Task colour is
  ready/blocked/closed, arrows are dependencies, and a dashed line is a repo a
  project writes about without listing it; a halo marks a node with a warning
  from the checks. Across projects, each project is a box around its tasks; with
  one project selected, tasks flow left to right by dependency, so the order the
  work has to happen in reads off the page. Clicking a node opens a side panel
  with everything known about it — for a task its source, blockers, repos,
  entries by kind and the latest entries; for a repo every project that works on
  it and its findings across all of them. Double-click focuses a neighbourhood;
  the URL keeps the view, so it can be bookmarked. Drawn with Cytoscape.js,
  vendored under `internal/web/static/vendor` (see `LICENSES.txt` there).
- **Checks** (`/checks`, also printed by `agentctx doctor`) lists what makes
  recorded knowledge harder to reach: repos in use but missing from the project
  note, tracker keys no project claims (or two claim), a repo logged under a
  short and a qualified name, entries naming a task that does not exist,
  dependencies that can never resolve, tasks opened with nothing logged, and
  notes with a long unconsolidated backlog. Advice, never enforcement — each is
  a decision for a person.

Read-mostly, deliberately. Obsidian edits notes better than a browser form, and
everything an agent writes goes through the verbs — so what this does is what
neither does well: the cross-cutting log view (which is where "did this host
write anything" gets answered), the graph and checks across projects, and
creating a project, the one write action worth a form.

Bound to loopback and unauthenticated. It also checks the `Host` header, because
a loopback bind stops another machine connecting but not a web page resolving a
name it controls to `127.0.0.1` and reading the vault through your browser.

## Vault layout

```
<vault>/
  proj/
    proj.md                                      project note, created deliberately
    notes/                                       consolidated per-repo knowledge
    tasks/proj-142.md                            created lazily by open_task
    log/2026-09-19T1432-proj-142-decision-a3f0.md   append-only, one file per entry
```

`log/` is the source of truth. Note bodies are consolidated views of it, written
only by `consolidate` — every other verb appends a file and touches nothing.
Until a note is consolidated it reads as empty in Obsidian however much has been
logged against it, so `open_task` says so once entries pile up.

## Design constraints

- **Append-only.** The vault sits on a file-sync service: last-write-wins, no
  locking, parallel sessions normal. Two writers must produce two files, never one
  corruption. Creation is atomic and refuses to clobber.
- **`open_task` never rewrites the task note.** It appends an `opened` entry, which
  records more (every open, with host and time) and keeps one writer per file.
- **Nothing enters without provenance.** Confidence is derived from the source kind
  rather than asserted; a caller may lower it, never raise it.
- **Dependencies are events, not a field.** A closed blocker stops blocking by
  itself, because the graph is folded out of the log rather than stored — no edge
  to clear, nothing to go stale, and the ordering that used to apply stays
  readable.
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
