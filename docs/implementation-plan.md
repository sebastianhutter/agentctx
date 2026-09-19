# agentctx — implementation plan

Derived from personal design notes kept in the vault (2026-09-19, including the
"Revised design — self-filling notes" section that supersedes the earlier ingest
design), plus a review of those assumptions against a live Kepler session and
against how `local-rag` is built.

Names in this document are placeholders — see the naming rule in `CLAUDE.md`.

Status: **phases 0–3 built** (2026-09-19). The five verbs work over both MCP and
the CLI, with 60+ tests. What remains is phase 4 (registration, hook, first real
project) and the two-week trial. Deviations from this plan as first written are
noted in §8.

---

## 1. What this is, in one paragraph

A durable, agent-writable knowledge layer for work that spans repositories and
trackers. Agents log **decisions** and **findings** during the work; each entry
records where the fact came from and when. Entries are append-only markdown files
in a dedicated Obsidian vault. Findings are scoped `task`, `repo` or `project`, and
the repo- and project-scoped ones are the point: they outlive the task that
discovered them, so the second task in a project is cheaper than the first.

It is **not** a task tracker, an aggregator, or an integration hub. Those exist
already (Jira, GitLab, Kepler, `local-rag`).

---

## 2. Decisions locked before writing code

| # | Decision | Rationale |
|---|---|---|
| D1 | **Go**, single static binary, pure Go (no CGO) | Matches `local-rag`. Nothing here needs SQLite, vectors or Ollama, so `make build` stays trivial and cross-platform |
| D2 | **Configuration is one JSON file** at `~/.agentctx/config.json`, defaults applied for absent fields, `~` expansion, annotated `configs/config.example.json` | Direct copy of `local-rag`'s `internal/config` shape |
| D3 |  **Dedicated vault**, beside the main one (real path in the local config only; `~/Obsidian/agentctx` in examples) | Already exists and is empty. Being a separate vault, the notes' `agent/` prefix is dropped: project folders sit at the vault root |
| D4 | **agentctx has no external interfaces.** No Jira client, no GitLab client, no `local-rag` client, no network calls at all | The agent calling the verb already has those MCPs. Required tool fields make it supply the facts with provenance. One less credential store, one less failure mode, and the tool contract does the work instead of a second integration surface |
| D5 | **Hosts in v1: Claude Code (direct and inside Kepler) + Claude Desktop** | Claude Code captures, Desktop reads and consolidates. Hooks are therefore fair game. The MCP schema stays the contract, so OpenCode/Codex cost nothing to add later — but v1 makes no sacrifices for them |
| D6 | **v1 surface: stdio MCP + CLI**, one binary, two front ends over one core | The CLI is what a `SessionStart` hook calls and what makes the thing debuggable by hand. Web UI deferred (the notes say build it after the MCP) |
| D7 | **The log is the only write surface.** Note *bodies* are written solely by the consolidation pass | Straight from the notes. Preserved without change |
| D8 | **`open_task` never rewrites the task note.** It appends an `opened` log entry | New — see §4.3. Removes the last-write-wins risk on a SynologyDrive-synced file entirely |
| D9 | **Confidence is derived from `source.kind`**, with an agent-supplied value allowed only to lower it | The notes' own reasoning: derived is harder to game. Precedence: `file` > `gitlab-mr` > `jira` > `confluence` > `slack` > `conversation` > `inferred` |
| D10 | **Staleness is computed from dates only** (`source.retrieved` age, `inferred` always flagged) | Follows from D4: a commit-vs-HEAD check needs a repo path and a git call. The stored `ref` is returned so the agent can verify in one command if it cares |

### Deferred to v2, deliberately

Consolidation verb and Desktop skill · `Stop` and `PreCompact` hooks · the
localhost web UI · near-duplicate supersede (`replaces:`) · a derived SQLite index
· any external interface, if D4 ever proves too austere.

---

## 3. Review findings that changed the design

Checked inside a live Kepler session rather than assumed.

### 3.1 Kepler already has a task-scoped memory layer

The notes' premise "Kepler has no layer 3" no longer holds. The workspace MCP
exposes `add_task_note` / `update_task_note` / `get_task_context`, documented as
cross-session memory, user-visible and **injected into every session of the task**
automatically — lower friction than a vault note, since nothing has to call a tool
to read it.

What Kepler still cannot do, and what agentctx is therefore *for*:

- nothing outlives the task — the next task in the same repo starts empty
- no repo or project scope — nowhere to put a fact a *different* task needs
- Kepler-only — Desktop sees none of it

**Effect on the design:** `scope: repo` and `scope: project` are the product, not a
feature. `scope: task` is the weakest verb inside Kepler and the working agreement
should say so: log a task-scoped entry only for a decision worth defending later;
for in-flight state a Kepler task note is cheaper. Without that sentence, two
systems get half-written and both get abandoned.

### 3.2 The Kepler branch name does not carry the issue key

The notes' resolution chain parses a Jira/GitLab key from the branch, "since Kepler
creates it from the issue". Verified false in the general case: this task's branch
is `please-review-the-notes-in` — a slug of the **task title**. A key appears only
when the task was started from an issue. A wrong parse attaches entries to the
wrong task silently, which is exactly the failure the chain was meant to prevent.

### 3.3 Kepler exports identity in the environment — use that instead

A stdio MCP spawned by Claude Code inside Kepler inherits:

```
KEPLER_TASK_ID=<uuid>            # stable per Kepler task, across all its sessions
KEPLER_SESSION_ID=<uuid>
KEPLER_REPO_ID=                  # empty in a multi-repo task — do not rely on it
KEPLER_WORKTREE_ID=              # empty in a multi-repo task — do not rely on it
KEPLER_BREADCRUMB_DIR=~/Library/Application Support/Kepler/worktree-breadcrumbs
```

Two consequences:

1. Identity resolution reorders so the unreliable step is last, not load-bearing
   (§4.4).
2. **Host detection is free, and the notes' measurement plan needs it.** Claude
   Code direct and Claude Code in Kepler share one `~/.claude/` MCP registration,
   so a hand-set `AGENT_HOST` would stamp the same value for both and the number
   actually wanted — "does Kepler produce log entries?" — would be unmeasurable.
   Presence of `KEPLER_TASK_ID` *is* the discriminator. `AGENT_HOST` survives as an
   override for Desktop.

There is also a worktree→task breadcrumb store keyed by a hash of the worktree
path, which would give cwd→task resolution for processes Kepler did not spawn.
Unverified — reading one was blocked by the sandbox. Not needed for v1.

### 3.4 `git config --worktree agent.task` does not work

Verified: `fatal: --worktree cannot be used with multiple working trees unless the
config extension worktreeConfig is enabled`. Enabling it writes
`extensions.worktreeConfig` into the **shared** `.git/config` — agentctx mutating
the user's repositories as a side effect of `open_task` — and it cannot express
this case anyway: one Kepler task, two worktrees, one value per worktree.

**Replacement:** `~/.agentctx/bindings.json`, keyed by both `KEPLER_TASK_ID` and
absolute worktree path. No repo mutation, works outside git, works for multi-repo
tasks, throwaway and rebuildable.

### 3.5 Kepler hands over `repos` and links for free

Kepler generates `~/kepler/tasks/<KEPLER_TASK_ID>/CLAUDE.md` and an identical
`AGENTS.md`, listing every worktree with its repo name and base branch plus every
attached link, under the header *"Generated by Kepler for this task. Do not edit:
this file is overwritten whenever the task's resources change."*

A credential-free, deterministic source for a task note's `repos` — which is part
of why D4 costs so little inside Kepler. Two riders: do not write the working
agreement into that file, it gets overwritten (a committed per-repo `AGENTS.md` is
a different file and unaffected); and Kepler's `attach_link` already covers
PR/issue association inside Kepler.

### 3.6 Confirmed unchanged

- `~/.claude/`-level MCP registration **is** reachable from a fresh Kepler worktree.
- `discard_worktree` deletes the worktree *and* its branch — anything stored inside
  a checkout dies with it. Keeping knowledge in the vault is right.
- Append-only file-per-entry remains correct and load-bearing: SynologyDrive is
  last-write-wins with no locking, and parallel sessions are normal.

---

## 4. Design

### 4.1 Vault layout

Vault root is the dedicated vault; one folder per project.

```
<vault>/
  proj/
    proj.md                                      project note, created deliberately
    notes/
      acme-platform.md                            repo-scoped knowledge
      acme-infrastructure.md
      conventions.md
    tasks/
      proj-142.md                                created lazily by open_task
    log/
      2026-09-19T1432-proj-142-decision-a3f0.md  append-only, one file per entry
```

Project-local everything: browse it, archive it, index it, hand it over.

### 4.2 Frontmatter contracts

**Task note** — written once at creation, thereafter only by consolidation:

```yaml
---
id: proj-142
source: jira                     # jira | gitlab | manual
source_key: PROJ-142
source_url: https://example.atlassian.net/browse/PROJ-142
title: …                         # supplied by the agent, not fetched
project: proj
repos: [acme/infrastructure, acme/platform]
kepler_task_id: <uuid>           # when opened from Kepler; correlation only
created: 2026-09-19
consolidated_through:            # empty until the first consolidation
---
```

Absent by design: `status`, `mrs`, `blocked_by` (live state, never stored — and
under D4 not even fetched), and `last_opened` / `last_host` (derived from the
`opened` entries in the log, see D8).

Body sections, empty at creation, maintained by consolidation:
`## Current understanding` · `## Decisions` · `## Findings` · `## Open threads`

**Project note** — `id`, `name`, `jira_projects`, `gitlab_groups`, `repos`,
`confluence_spaces`, `created`, `consolidated_through`. The `jira_projects` /
`gitlab_groups` lists are the key-prefix → project mapping. Projects are few and
created deliberately; tasks are many and created lazily.

**Log entry** — `kind` (`opened` | `decision` | `finding` | `closed` | `note`),
`task`, `project`, `scope`, `repo` (required when `scope: repo`), `ts`, `host`,
`session`, `source{kind, ref, retrieved}` (required on findings), `confidence`.
Body is the decision or finding itself.

Filename: `<ts>-<task>-<kind>-<4 hex>.md`. The random suffix removes the
same-second collision case for parallel hosts.

### 4.3 Why `open_task` stops writing the task note (D8)

The notes have `open_task` update `last_opened` and `last_host` on every call. Two
parallel Kepler sessions on one task then read-modify-write the same file on a
SynologyDrive path — the exact hazard the append-only rule exists to avoid,
reintroduced by the one verb every session calls first.

Appending an `opened` entry instead gives strictly more (every open is a fact with
a host and a timestamp, not just the latest) and keeps a single writer per file.
The task note becomes: written once at creation, then only by consolidation, which
is single-writer by nature. Frontmatter mutation disappears from the hot path.

### 4.4 Identity resolution

`task_id`, in order — first hit wins, no cwd inference ever:

1. explicit argument
2. `bindings.json` lookup by `KEPLER_TASK_ID`
3. `bindings.json` lookup by absolute worktree path
4. Jira/GitLab key parsed from the branch name — best effort, often absent (§3.2)
5. refuse, and say which of the above to supply

On success with an explicit argument, record the binding for 2 and 3 so later calls
in the same task need no argument.

`project`: from the task key prefix via `jira_projects` / `gitlab_groups` across the
project notes. Unknown prefix, or a task with no key at all (a title-only Kepler
task), requires an explicit `project` argument. **Projects are never auto-created** —
refuse with "run `open_project` first".

`host`: `AGENT_HOST` if set, else `kepler-claude-code` if `KEPLER_TASK_ID` is
present, else `claude-code` if `CLAUDECODE` is set, else `unknown`.

### 4.5 Verbs

```
open_project(project_id, name?, jira_projects?, gitlab_groups?, repos?, confluence_spaces?)
    -> project bundle; creates the note if missing (args required on create)

open_task(task_id?, project?, seed?{source, source_key, source_url, title, repos})
    -> task bundle + working agreement; creates the note if missing.
       On create, seed is required: the agent looks the issue up with whatever
       tracker MCP it has and passes the facts in. The task's first fact is
       where it came from.

log_decision(task_id, decision, rationale, alternatives_rejected[], affected_repos[])

log_finding(target_id, finding, source{kind, ref, retrieved}, scope, repo?, confidence?)
    target_id is a task or a project; scope=repo requires repo.
    A project target is what lets a Desktop "what do we know about X" session
    write back — the read side is also a write side.

close_task(task_id, summary)
    -> refuses if no decision or finding was logged since the last open
```

### 4.6 What `open_task` returns

Under D4 the bundle is purely what is stored, plus arithmetic:

1. task frontmatter, and the consolidated body if `consolidated_through` is set
2. task-scoped entries newer than `consolidated_through` (body first, then newer
   entries — so consolidation is compaction, never correctness)
3. project-scoped findings for the task's project
4. repo-scoped findings for each of the task's `repos`
5. staleness markers: `source.retrieved` older than `stale.max_age_days` ⇒
   `re-verify`; `source.kind: inferred` ⇒ always flagged, never presented as fact.
   Each `file` finding returns its stored `ref` (`path@commit`) so the agent can
   check it against HEAD itself in one command if the finding matters.
6. the working agreement: log at decision points not session end; provenance is
   required; `close_task` will refuse an empty session; prefer `repo`/`project`
   scope for anything that outlives this task; a task-scoped note that is really
   in-flight state belongs in Kepler's own task notes, not here.

Gone relative to the notes: live tracker state (step 4) and `local-rag` suggested
reading (step 7). A first bundle in a new project is therefore close to empty. That
is honest: the system pays off from the second task in a project onward, which is
what the two-week trial actually tests.

---

## 5. Repository layout

Mirrors `local-rag` so both repos read the same way.

```
agentctx/
├── CLAUDE.md
├── Makefile                      # build, test, lint, tidy — no CGO, no build tags
├── README.md
├── go.mod / go.sum               # cobra, mcp-go, yaml.v3 — that is all
├── cmd/agentctx/
│   ├── main.go                   # cobra root, --verbose, version
│   ├── cmd_serve.go              # stdio MCP (--port for SSE later)
│   ├── cmd_open.go               # open-project, open-task (the hook entry point)
│   ├── cmd_log.go                # log-decision, log-finding
│   ├── cmd_close.go              # close-task
│   └── cmd_doctor.go             # validate config, vault, identity, host detection
├── scripts/check-names.sh        # Rule 0 enforcement, wired into make lint and CI
├── configs/config.example.json
├── docs/
│   ├── implementation-plan.md    # this file
│   ├── architecture.md
│   ├── vault-schema.md           # frontmatter contracts, entry shape, filenames
│   ├── kepler-integration.md     # env vars, bindings, the generated task CLAUDE.md
│   └── enforcement.md            # why required fields + refusal; what is not enforced
├── internal/
│   ├── config/                   # config.json load/save/defaults, ~ expansion
│   ├── vault/                    # paths, atomic write, frontmatter parse/render
│   ├── store/                    # notes and entries: create, append, read, scope queries
│   ├── bundle/                   # bundle assembly, staleness, working-agreement text
│   ├── identity/                 # task/project resolution, Kepler env, bindings, host
│   ├── service/                  # the five verbs — one implementation, two front ends
│   └── mcp/                      # server.go (instructions) + tools.go (five verbs)
└── .github/workflows/release.yml
```

Coding standards adopted wholesale from `local-rag`: doc comments on exported
identifiers, structs not untyped maps in public APIs, wrapped errors rather than
panics, no global state, `log/slog` to stderr, tests beside the code they test.

`log/slog` to **stderr** is load-bearing for a stdio MCP: anything on stdout
corrupts the protocol.

### Configuration

```json
{
  "vault_path": "~/Obsidian/agentctx",
  "state_dir": "~/.agentctx",
  "host": "",
  "stale": { "max_age_days": 30 },
  "confidence": { "derive_from_source": true }
}
```

Everything else is a default in code. `state_dir` holds `bindings.json` and is
never inside the vault — it must not be synced.

---

## 6. Build order

| Phase | Scope | Est. |
|---|---|---|
| **0 — scaffold** ✅ | `go.mod`, Makefile, cobra skeleton, `internal/config` + example JSON, `CLAUDE.md`,  `agentctx doctor` printing resolved config, vault path, host and identity, plus `make check-names` (deny-list grep over tracked files) wired into `make lint` | ~½ day |
| **1 — vault and store** ✅ | frontmatter parse/render, atomic create-and-rename writes, project note create, task note create, entry append, scope queries. Tests run against a temp-dir vault, so no real vault is touched | ~1 day |
| **2 — identity and bundles** ✅ | resolution chain (§4.4), Kepler env detection, `bindings.json`, host detection, bundle assembly, date-based staleness, working-agreement text | ~1 day |
| **3 — MCP and CLI** ✅ | five verbs as thin wrappers over the core, server instructions, `close_task` refusal, the same verbs as CLI subcommands | ~1 day |
| **4 — wire up** | register in `~/.claude/`, `SessionStart` hook calling `agentctx open-task`, Kepler Action "call `open_task` first", `open_project("proj")` by hand, one Bases view, add the vault to `local-rag` so entries are retrievable | ~½ day |
| **5 — trial** | two weeks of real work on one project. Measure: entries per host, tasks opened vs tasks written to, and whether the bundles get read | 2 weeks |
| **v2** | only if the log is being read: consolidation verb + Desktop skill, `Stop` and `PreCompact` hooks, web UI, near-duplicate supersede | — |

Phase 3 is the first point where the thing is usable. Phases 0–3 are about four
days of work, and nothing before phase 4 touches the real vault.

### Consolidation, when it arrives

Agents never write note bodies, so consolidation needs a verb too — a Desktop skill
reading a task's log and submitting composed body sections through
`consolidate(target_id, sections{…})`, which also sets `consolidated_through`. Log
entries are never edited or deleted. One write path, no drift between what the
agent sees and what a human sees. Do it by hand a few times before automating it.

---

## 7. Open questions

Resolved by this plan:

- Tracker fetch: own tokens vs seed payload → **neither; no interfaces at all (D4)**.
  Facts enter through required tool fields, supplied by an agent that already has
  the interface.
- Confidence: agent-declared vs derived → **derived from `source.kind`**, agent may
  only lower it (D9).
- Web UI storage: SQLite vs files → **files stay the source of truth**; SQLite only
  ever as a throwaway index under `~/.cache/`, and not in v1.

Still open:

- **Near-duplicate findings** — refuse, or supersede with a `replaces:` link?
  Leaning supersede; the log stays append-only either way. Needs real duplicates to
  decide, so it waits for the trial.
- **Retention** for closed tasks — leave everything and let retrieval sort it out,
  or compact? A non-problem until it is one.
- **Write-back to the tracker** — under D4, agentctx cannot. If a Jira comment
  summarising a decision is ever wanted, the agent does it with its own Jira MCP.
  Probably the right split; worth revisiting only if it is missed.
Resolved after the first draft:

- **Repository visibility** — private now, possibly public later. Because git
  history is permanent, the no-names rule applies from the first commit rather than
  from the day visibility changes. It is Rule 0 in `CLAUDE.md`, and this document
  uses placeholders throughout (`proj`, `PROJ-142`, `acme/infrastructure`,
  `example.atlassian.net`). The real vault path lives only in
  `~/.agentctx/config.json`; `configs/config.example.json` carries a neutral one.
  Phase 0 adds a `make check-names` grep of a deny-list over tracked files, wired
  into `make lint`, so the rule is enforced by the build rather than by memory.


---

## 8. What changed while building it

Four departures from the plan as written, each for a reason found in the code
rather than at the desk.

**A `service` package was added.** The plan had the MCP server wrapping the core
directly, but the CLI needs the same five verbs with the same refusals, and
duplicating `close_task`'s "did this session log anything" check in two places is
exactly the drift the one-write-path rule exists to prevent. `internal/service`
holds the verbs; `internal/mcp` and `cmd/agentctx` are both thin over it.

**`vault.Create` links rather than renames.** Rename is atomic but silently
clobbers, which would turn a filename collision between two parallel sessions
into a lost entry. `os.Link` is atomic *and* fails when the target exists, so a
race is reported and retried instead of resolved. Covered by a test that points
24 concurrent writers at one filename.

**Dates are emitted unquoted.** `yaml.Marshal` quotes a date held in a Go string,
and a quoted value is a string to Obsidian Bases — unsortable and unfilterable as
a date, in the views the plan calls for. `vault.Render` unquotes ISO dates on the
way out. Trivial now, a migration across every file later.

**`check-names.sh` uses structural patterns, not a list of names.** The obvious
implementation is a deny-list of the real names — which is itself the leak, once
committed. So the committed check matches the *shape* of a leak (issue keys
outside a closed placeholder set, tracker hostnames, mail addresses, home
directories), and the literal names live in `~/.agentctx/deny-names.txt`, outside
the repository, where the check picks them up if present.

### Still to do

Phase 4 (register in `~/.claude/`, `SessionStart` hook, first real project, one
Bases view, add the vault to `local-rag`) and then the two-week trial. The v2 list
in §2 is unchanged.
