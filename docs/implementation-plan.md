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
| D1 | **Go**, single binary, macOS only, CGO for the menu bar app | Matches `local-rag`, including its build. Was pure Go until the menu bar app arrived — see §8 |
| D2 | **Configuration is one JSON file** at `~/.agentctx/config.json`, defaults applied for absent fields, `~` expansion, annotated `configs/config.example.json` | Direct copy of `local-rag`'s `internal/config` shape |
| D3 |  **Dedicated vault**, beside the main one (real path in the local config only; `~/Obsidian/agentctx` in examples) | Already exists and is empty. Being a separate vault, the notes' `agent/` prefix is dropped: project folders sit at the vault root |
| D4 | **agentctx has no external interfaces.** No Jira client, no GitLab client, no `local-rag` client, no network calls at all | The agent calling the verb already has those MCPs. Required tool fields make it supply the facts with provenance. One less credential store, one less failure mode, and the tool contract does the work instead of a second integration surface |
| D5 | **Hosts in v1: Claude Code (direct and inside Kepler) + Claude Desktop** | Claude Code captures, Desktop reads and consolidates. Hooks are therefore fair game. The MCP schema stays the contract, so OpenCode/Codex cost nothing to add later — but v1 makes no sacrifices for them |
| D6 | **v1 surface: menu bar app + MCP (stdio and SSE) + CLI**, one binary, three front ends over one core | The menu bar app owns the shared server's lifetime and is where the web interface will be opened from. The CLI is what a `SessionStart` hook calls. See §8 for what the SSE transport costs |
| D7 | **The log is the only write surface.** Note *bodies* are written solely by the consolidation pass | Straight from the notes. Preserved without change |
| D8 | **`open_task` never rewrites the task note.** It appends an `opened` log entry | New — see §4.3. Removes the last-write-wins risk on a SynologyDrive-synced file entirely |
| D9 | **Confidence is derived from `source.kind`**, with an agent-supplied value allowed only to lower it | The notes' own reasoning: derived is harder to game. Precedence: `file` > `gitlab-mr` > `jira` > `confluence` > `slack` > `conversation` > `inferred` |
| D10 | **Staleness is computed from dates only** (`source.retrieved` age, `inferred` always flagged) | Follows from D4: a commit-vs-HEAD check needs a repo path and a git call. The stored `ref` is returned so the agent can verify in one command if it cares |

### Deferred to v2, deliberately

Desktop skill for consolidating · `Stop` and `PreCompact` hooks · a derived
SQLite index · any external interface, if D4 ever proves too austere.

(Explicit supersede, once on this list as `replaces:`, has since been built as
`supersedes`, and project amendments as `amend` entries folded on read — see
`docs/vault-schema.md`. What prompted it was a finding filed
under a bare repo name: the one kind of mistake the checks can see and nothing
could repair. Automatic near-duplicate detection stays out.)

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

### 4.4 Identity

Two different questions, and only one of them is worked out here.

**Which task is meant** is never inferred. It is a required argument on every
verb that writes. An earlier version resolved it from a recorded binding, the
worktree path, or a tracker key parsed out of the branch name; that is gone.
See §10.

**Which host is writing** is detected, because it has no comparable failure
mode — a mis-stamped host makes one number slightly wrong, where a mis-resolved
task files work under the wrong heading and nobody notices. Over stdio and the
CLI it comes from the environment (`AGENT_HOST`, else `KEPLER_TASK_ID` present
means a task runner, else `CLAUDECODE`). Over the shared SSE server the
environment is the menu bar app's, so the name the MCP client announced at
`initialize` is used instead.

`project` is resolved from the task key prefix via `jira_projects` /
`gitlab_groups` across the project notes. An unknown prefix, or a task with no
key at all, requires an explicit `project` argument. **Projects are never
auto-created** — refuse with "run `open_project` first".

### 4.5 Verbs

```
find(query?, repo?, ready_only?, include_closed?, limit?)
    -> project and task ids, with why each matched. Never their contents.

open_project(project_id, name?, jira_projects?, gitlab_groups?, repos?, confluence_spaces?)
    -> project bundle; creates the note if missing (args required on create)

open_task(task_id, project?, seed?{source, source_key, source_url, title, repos})
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
| **0 — scaffold** ✅ | `go.mod`, Makefile, cobra skeleton, `internal/config` + example JSON, `CLAUDE.md`,  `agentctx doctor` printing resolved config, vault path, host and identity | ~½ day |
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
- **Does the second task cost less?** It is the design's central claim and
  nothing measures it. Asking the agent to estimate its tokens at `close_task`
  would give a number no agent can see, and a task spans sessions no single
  agent sees whole — a guess, which D9 already says not to trust. If it is
  measured, it is measured: the host's own transcripts carry exact usage, a
  `SessionEnd` hook could pass it to the CLI as one entry per session, and the
  total is folded on read. agentctx can also count its own share exactly — the
  size of every bundle it returns.
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
  Nothing enforces it automatically — see §8 for why the attempt was dropped —
  so it is a writing rule plus a read-through before the repository is published.


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

**The automated name check was built and then dropped.** A committed script
cannot hold the list it needs to check — a deny-list of real customer names is
itself the leak — so the version that existed matched the *shape* of a leak
(issue keys outside a closed placeholder set, tracker hostnames, mail addresses,
home directories) and read the literal names from a file outside the repository.
It worked, and it was still the wrong thing to carry: a false-positive machine
guarding a rule that applies to prose a human writes, in a one-person repository,
where the real safeguard is reading the diff. Rule 0 stays as instructions in
`CLAUDE.md` with a read-through before the repository goes public. Revisit only
if a name actually slips.

### Still to do

Phase 4 (register in `~/.claude/`, `SessionStart` hook, first real project, one
Bases view, add the vault to `local-rag`) and then the two-week trial. The v2 list
in §2 is unchanged.


---

## 9. The menu bar app, and what the SSE transport costs

Added after phases 0–3, to match how `local-rag` is run: a menu bar app that
owns a long-lived MCP server over SSE, so a client registers one endpoint —
`http://localhost:31124/sse` — instead of being configured to spawn a process.
The menu starts, stops and restarts it, copies the endpoint, shows what is in
the vault, and has the settings dialog. The web interface, when it exists, gets
opened from the item already sitting in that menu.

This reverses the design note's "stdio, not a daemon", and the reasoning there
was not wrong: file-per-entry needs no coordination, so a daemon buys no
locking or caching worth having. What it buys is ergonomics — one registration,
a visible on/off, somewhere for the web UI to live — which is a fair trade as
long as the cost is understood, because the cost is not obvious.

**A shared server cannot see who is calling it.** Over stdio the host spawns one
process per session, so the working directory and `KEPLER_TASK_ID` belong to the
agent doing the work, and `open_task` can resolve a task from a binding or the
branch name. Over SSE one process serves everyone, and its environment is the
*menu bar app's*. Resolving a task from it would attribute entries to whatever
the desktop session happened to look like — silently, and in exactly the way the
identity design exists to prevent.

So `service.Transport` makes the difference explicit, and over SSE:

- **`task_id` is required.** Environment resolution refuses, with an error that
  says why and what to pass.
- **The host is the MCP client's declared name** (`claude-ai/0.1.0`), taken from
  the session, not from `getenv`. Without this every entry would carry the same
  wrong host and the drift measurement could not be made at all.
- **The session id is the MCP session's**, for the same reason.
- **No binding is recorded**, and `kepler_task_id` is left empty rather than
  filled with the server's own.

Each of those is a test. The practical advice, which the settings dialog and the
server instructions both give: **prefer a stdio registration where a host
supports one** — it is the transport that can work out what you mean. Use the
shared endpoint for hosts that only speak HTTP, and pass `task_id`.

### Build

macOS only now, and CGO-enabled, because the menu bar app is Fyne and Fyne binds
to Cocoa. The cross-compiled linux and amd64 targets are gone: they were free
while the binary was pure Go, and they are not free now, and nothing was using
them. `make app` and `make dmg` produce the bundle and installer; CI and the
release workflow run on `macos-latest`, as `local-rag`'s do.

One thing worth knowing about the DMG build, because it cost an hour: the
one-shot `hdiutil create -srcfolder` that `local-rag` uses fails with
"Resource busy" when the staging folder holds a Mach-O binary that macOS decides
to scan while hdiutil is reading it. Measured directly — 25MB of random bytes
images fine, the same bytes marked as an executable do not. `build-dmg.sh`
therefore creates a read/write image, copies into the mounted volume with `cp`,
detaches with retries, and converts. Slower by a second and reliable.


---

## 10. Removing the task resolution chain

The chain — explicit argument, then a binding in `~/.agentctx/bindings.json`,
then the worktree path, then a tracker key parsed out of the branch name — is
gone. `task_id` is a required parameter on every verb that writes, and
`log_finding` requires `target_id`.

It was 484 lines including its tests, plus a state file, plus most of the
transport-awareness added with the SSE server, and it existed to save an agent
from repeating a string that `open_task` prints as the first line of its own
output. Against that: the chain could attribute an entry to the wrong task with
nothing looking wrong, which is the only failure this system can neither detect
nor repair. Everything else it does is append-only and reviewable; a
mis-resolved task is a fact filed under the wrong heading, found weeks later if
at all.

It was also already inert where it mattered. The shared SSE server — the way
this is actually run — could never use the environment, because the environment
belongs to the menu bar app. So the machinery served one transport, guarded by a
second mechanism, to avoid one argument.

The tool schema is the right place to say what a call needs. Every host reads it
identically, a required field cannot be forgotten, and there is no inference to
be wrong.

What was lost with it: a `SessionStart` hook that could open the current task
with no arguments. That was the one genuine use — a deterministic lookup at the
start of a session for the strongest host. It can come back the moment it is
wanted, as an explicit id in the hook command or a per-repo file naming the
current task, without any of the guessing. The rest of the plan is unaffected:
`AGENT_HOST` detection stays, `open_project` is unchanged, and no vault file
changes except that task notes no longer carry `kepler_task_id`, which only ever
existed to correlate with a binding.


---

## 11. find

A sixth verb, added because §10 removed the last way to get an id without being
told one. Every other tool requires a `task_id` or `project_id`; an agent that
was handed neither could not list what exists, and `open_project`'s refusal —
"run open_project first" — is useless advice to something that cannot see what
is already there.

**It returns places to look, not what is there.** Ids, titles, repositories, and
one line saying why each matched. The caller then opens one, which is where the
actual knowledge comes from, with provenance and staleness intact.

That shape is what lets it search logged text without becoming a retrieval
engine. A query describing a problem — `find provider upgrade` — matches the
body of a finding and surfaces *the task that holds it*, costing a few hundred
tokens rather than a page of content. Matching is literal, case-insensitive,
and every term must appear.

**It is not semantic, and deliberately so.** A vault indexed by a proper
retrieval tool already answers "what is this like" far better than anything that
could be built here without an embedding model and a network call, both of which
D4 rules out. This answers "what is it called", which is the question that has to
be answered before any other tool can be used.

Two details worth keeping:

- **Closed tasks are excluded** unless asked for. A closed task is rarely the one
  to log against, and including them by default buries the open work.
- **`opened` entries are not searched.** Their bodies are boilerplate this tool
  wrote itself, containing the host name — searching them would make every task
  match a query for `kepler`, confidently and uselessly.


---

## 12. Task dependencies

The original notes had `blocked_by` in the task note frontmatter and then took
it out, with a reason that still holds:

> Gone: `status`, `mrs`, `blocked_by`. All three are **fetched live** by
> `open_task`, never stored. Stored state drifts; a fetch does not.

Then D4 removed the fetch, and dependencies ended up neither stored nor fetched
— a gap made by two decisions that were each right on their own, the same shape
as the discovery gap `find` fills.

**Dependencies are events, not a field.** `log_dependency` appends an entry
naming what a task waits for and why; the current graph is folded out of the log
the same way open/closed already is. That answers the drift objection rather
than ignoring it:

- A **closed blocker stops blocking by itself.** Nobody has to remember to clear
  an edge, which is exactly what a stored `blocked_by` list would require and
  would not get.
- **A change of mind is a later entry.** `resolved: true` lifts an edge without
  editing anything, and the ordering that used to apply stays readable.
- **The reason is required.** An ordering nobody can justify is one nobody can
  safely remove later, and "why is this blocked" is the question a future
  session actually has.

### What it refuses

- **Unknown task ids.** A mistyped blocker holds up work that nothing is
  actually waiting for, and the typo is invisible. `open_task` it first — it
  needs a title anyway, and then there is somewhere for what you learn about it
  to go.
- **Cycles**, with the path named: `proj-1 → proj-3 → proj-2 → proj-1`. A loop
  leaves no task that can be started first, so "work in the right order" becomes
  unanswerable. Cheaper to reject the edge than to explain the deadlock later.
- **Self-dependency**, and edges across projects, which are not modelled.

### Where it shows up

- `open_task` leads with **⚠ Blocked** and the unfinished blockers, before
  anything else about the task — starting blocked work is the expensive mistake
  this exists to prevent. Once a blocker closes it moves to a quieter
  "waited for: … (closed)" line, so the history survives the unblocking.
- `open_project` splits open work into **Ready to start** and **Blocked**.
  Separated rather than filtered, because "nothing to do" and "nothing you can
  do yet" call for different responses.
- `find --ready` / `ready_only` lists only what can be started now, and ready
  tasks sort above blocked ones everywhere else.

### Obsidian

The entry body names its blockers as `[[proj-139]]` wikilinks, so the vault's
own graph view shows the ordering with nothing else built. A Bases view can
filter the `log/` folder on `kind: dependency` and `resolved`. Folding the
current blockers into the task note's frontmatter — which is what Bases would
want for a ready-queue over tasks — belongs to the consolidation pass, since
that is the only thing allowed to rewrite a note.


---

## 13. Consolidation, moved forward

The plan had this after the two-week trial. The first real project moved it: 72
entries in three minutes, and every note body still empty. The knowledge was all
there and none of it was *readable* — `open_task` assembles a bundle live, so
the agent side worked, while opening the project in Obsidian taught you nothing.
A write-only vault fails the half of the design that says notes fill themselves
up over time.

`consolidate(target_id, body, repo?, expect_consolidated_through?)` writes a
composed body into a task note, a project note, or a repository's page under
`notes/`, and records how far the log has been folded.

**The caller composes the prose.** A mechanical fold would concatenate entries,
which is what the log already is. Consolidating means judgement — dropping what
was superseded, keeping the newest claim with its provenance, writing what is
now understood rather than what was said. So this verb takes the body and writes
it; it does not generate one.

**It is the only verb that rewrites anything**, which makes it the only place
two sessions could lose each other's work. `expect_consolidated_through` is an
optimistic check: pass what the bundle reported, and a fold composed from a
stale read is refused rather than silently overwriting the other one.

Three details found by testing it:

- **A fold covers its own record.** The consolidation entry is appended *before*
  `consolidated_through` is set. The other order leaves every consolidation's own
  note dangling as the one outstanding entry, for ever.
- **The boundary is inclusive.** Timestamps have second resolution, so an entry
  written in the same second as a fold would vanish into it under a strict
  `after` comparison. An entry on the boundary is now shown again instead —
  duplication is cheap, and compaction losing a finding is the one thing this
  must never do.
- **An empty body is refused**, including one that is only whitespace.
  Consolidating to nothing deletes what a note says.

### The nudge

`open_task` and `open_project` say how many entries are unfolded once there are
five or more, and name the target and `consolidated_through` to pass. The first
project showed the failure is not that folding is hard — it is that nothing ever
asks for it.


---

## 14. The web interface

`internal/web`: projects, a project page, task pages, repository notes, a log
view, a graph with a detail panel, the vault checks, and a form for creating a
project. Served on `127.0.0.1:31125` by the menu bar app, or standalone with
`agentctx web`. Go templates, one stylesheet, and no build step; the graph page
is the one place with JavaScript, using Cytoscape.js vendored under
`internal/web/static/vendor` so nothing is fetched from elsewhere.

**Read-mostly, as the notes scoped it.** Obsidian edits notes better than a
browser form will, and everything an agent writes goes through the verbs. Two
things this does that neither does well:

- **The log view.** Every entry, filtered by project, task, kind, scope or host,
  newest first, with facet counts. Obsidian reads one note at a time and Bases
  filters awkwardly across folders; this is where "which hosts actually wrote
  anything" gets answered.
- **Creating a project**, the one write action worth a form — the key-prefix
  mapping has to be typed by a person, and doing it through a chat to get a note
  written is a silly way to fill in four fields.

It also shows what is *not* consolidated: a project or task whose body is empty
says so, and the repository list marks which pages exist, which have findings
waiting to be folded, and which have nothing recorded. The same nudge as the
bundles, aimed at the person rather than the agent.

### Two security properties, neither theoretical

**Path traversal.** Ids become path segments, and `ProjectPath("../../etc")`
resolved to `/etc.md`. Harmless while only an agent could pass one; a file-read
hole the moment ids arrive from a URL. `store.ValidateID` now rejects separators,
`..`, leading dots, null bytes and over-long ids, and it is enforced at the store
API rather than in the web layer, so every caller gets it — including the one
added next year by someone who has not read this.

**DNS rebinding.** The listener is on loopback, which stops another machine
connecting. It does not stop a page in your browser resolving a name it controls
to `127.0.0.1` and reading the vault through you. A `Host` header check closes
that and costs nothing, which is worth doing before pointing a browser at a
folder containing everything you know.

### Found by writing the tests

- **Every page rendered as the same page.** Each page template defines
  `content`, and `html/template` keeps one definition per name in a set — so
  parsing them together left whichever file was read last answering for all of
  them. One template set per page.
- **The project page 500'd** on real data: it passed raw entries to a template
  expecting decorated ones.
- **A new project showed no repositories**, because the list only included repos
  that already had notes or findings. They are part of what a project *is*.
- **A freshly created note looked consolidated**, since its skeleton of empty
  headings is not an empty body. Headings with nothing under them do not count.
- **`[[proj-210]]` rendered as literal brackets.** The vault's own link syntax,
  which markdown does not know; it now resolves through `/t/<id>`, which finds
  the task's project so a note body does not have to carry one.


---

## 15. Repository findings across projects

A repository-scoped finding is "true about a repository, for whoever works on it
next" — but until now *whoever* meant whoever works on it next **in the same
project**. `open_task` and `open_project` read repo findings from the task's own
project folder only, so a second project working in `acme/platform` started
from nothing, however much the first had learned there.

That turned out to be the common case, not an edge. An audit of a real vault
after a week of use: 12 projects, 21 repositories with repo-scoped findings, and
8 of those repositories had findings in two or three projects — about 70
entries. Read one by one, none was misfiled. Projects are mostly workstreams (a
migration, a feature) over repositories the organisation owns, so several of
them touching one repository is what normal work looks like. The cost was
visible too: one fact logged twice in two projects, three minutes apart, and
four repositories with a consolidated note in each of two projects, each folding
half of what is known.

**Reads widen; writes do not move.** An entry is still appended to the project
whose task wrote it — append-only, one file per writer, nothing relocated. The
bundles additionally collect repo-scoped findings about the same repositories
from every other project, the same fold-on-read idea as amendments. The
repository id is what makes this safe: `acme/platform` names one repository
wherever it is logged. That is the property to check before doing the same for
anything else (§16).

**Shown apart, with where they came from.** Findings from elsewhere come under
their own heading, "What other projects know about `acme/platform`", each
marked with the project it was read from. Two reasons found in the audit, not
invented:

- Many repo findings are pinned to a moment the writing project shared — "on
  main@abc1234", "not merged yet, branch X". The reader needs to know whose
  moment that was.
- Some are written from the project's point of view: "reference repository,
  never modified by this project". Read from another project, *this project*
  means something else.

**Superseding one is done where it lives.** `supersedes` stays within a
project. A wrong finding from elsewhere is replaced with `log_finding` targeting
*that* project — a finding may target a project, which is exactly this case.

**Consolidation is unchanged, deliberately.** Each project's `notes/` page
still folds that project's entries only. A shared page per repository outside
the project folders is the cleaner end state, but it changes the vault layout
and the one-parent link scheme, and it is not yet clear the split pages hurt
once the bundles show both halves. Decided after this has been used, not before.

**Bounded as headlines.** A repository with many findings elsewhere made a
long bundle — the first in the audit had eighteen, and one bundle grew from
14 KB to 107 KB. Findings from other projects are therefore shown as their
first 160 characters (§16). If even that proves too much, the next cut is by
date, newest first, with a count of what was left out — never silently.


---

## 16. More subjects than repositories

Status: **built** (2026-09-27), ahead of the trial §15 was waiting for — see the
end of this section for what was settled while building it.

Asked while designing §15: should findings attach to more than repositories —
an **environment** (the staging and production accounts), a **service** (a SaaS
dependency, or a self-hosted one), **documentation** (a wiki page, a vault note)?

The test a new kind has to pass is the one repositories pass:

1. Durable findings are *about* it, not merely mentioning it.
2. An agent can name it by an id without guessing.
3. Something tells `open_task` it is relevant — a task's `repos` do that for
   repositories.

- **Environment — yes.** In the same audit, roughly a third of the durable
  findings mention staging or production. (That was a keyword count, and it
  overstated the case — see "Measured afterwards" below.) What is recorded is what was *learned* about an
  environment, never an inventory of it: live state is what `status` was, and
  under D4 nothing is fetched. The id is **not** global — every project has a
  `prod` — so an environment stays project-local.
- **Service — yes.** A SaaS name is global and would read across projects like
  a repository; a self-hosted instance belongs to one project. Which of the two
  a service is has to be part of its id.
- **Documentation — no.** A wiki page is not what a finding is about but where
  it came from, which is `source{kind, ref}`. As a kind of its own it turns this
  into a link collection, and the vault is already indexed by a retrieval tool
  that reads those pages.

The shape: one mechanism, not one per kind. `repo` is one case of a
`store.Subject` with a kind and an id; `environment` and `service` are two more
scopes, each with a field of its own on the entry (`environment:`,
`service:`) so the files still read plainly in Obsidian and filter plainly in
Bases.

Settled while building it:

- **Every subject id is global.** The first draft above made environments
  project-local because every project has a `prod`. The audit says otherwise:
  projects are workstreams, and five of them shared one organisation's staging
  account. So the id carries its owner instead — `acme/production`, enforced on
  write — and environments read across projects exactly as repositories do. A
  service is `idp` for the product or `acme/idp` for one
  organisation's instance; both read across projects, and the "from project"
  marker says whose finding it was.
- **Relevance, the third test: listed or logged.** A project lists its
  environments and services (`environments:` / `services:` on the note, changed
  by `amend_project`), and a bundle covers those plus any the project's own log
  has findings about. Listing is what brings in other projects' knowledge before
  anything is logged here; the log is what keeps a finding under an unlisted
  subject from disappearing. Every task in the project gets them, as it gets
  project-scoped findings — nothing hands a task its environments.
- **A subject is set with its own scope, and only then.** `environment` given
  with `scope: task` is refused, not re-scoped: the agent meant the finding to
  outlive the task, and the task-scoped default would lose it silently.
- **Pages per kind**, `notes/environments/` and `notes/services/`, so
  `acme/production` the environment and a repository of the same name cannot
  share a file. Repository pages stay where they were. Entries link to theirs
  through `subject_note`, so `repo_note` still means repositories.
- **Findings from elsewhere are headlines.** The size problem §15 left open was
  real: rendered in full, one bundle went from 14 KB to 107 KB. They now show
  their first 160 characters, and `open_project` on the project named under
  one gives the whole text.

Measured afterwards, by reading every one of the 316 durable findings in the
vault rather than grepping them: **17** were chiefly about an environment or a
service — 4 environments, 13 services, most of them the behaviour of a cloud
provider's product. The rest mention a stage while being about a
repository's code, a pipeline or the plan, and stay where they are. So the
keyword count was wrong about the past, and the case for the kinds rests on
two other things: **listing** — the survey proposed 14 environments and 20
services, 8 of them shared by two to five projects, each of which now reaches
every project that lists it — and the findings still to be written, which
until now had no scope to go to. The 17 were re-scoped by superseding, and the
lists added by `amend_project`, both through the verbs.
