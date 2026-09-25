# Vault schema

Everything agentctx writes is a markdown file with YAML frontmatter, in a
dedicated Obsidian vault. There is no database. The files are the source of
truth, and a directory walk with a frontmatter parse is milliseconds at the
volumes one person produces.

Names below are placeholders — see Rule 0 in `CLAUDE.md`.

## Layout

```
<vault>/
  proj/
    proj.md                                         project note
    notes/
      acme-infrastructure.md                        consolidated per-repo knowledge
      conventions.md
    tasks/
      proj-142.md                                   one per task
    log/
      2026-09-19T1432-proj-142-decision-a3f0.md     append-only, one file per entry
```

Project-local everything: a project folder is a self-contained unit that can be
browsed, archived, indexed or handed over on its own.

## Log entry

The only thing an agent writes. One file per entry, never edited, never deleted.

```yaml
---
kind: finding                  # opened | decision | finding | dependency | closed | note
task: proj-142                 # absent on a project-scoped entry with no task
project: proj
scope: repo                    # task | repo | project
repo: acme/infrastructure      # required when scope is repo
ts: 2026-09-19T14:32:07
host: kepler-claude-code
session: 077cbc02-…            # host session id, when the host exposes one
source:                        # required on findings
    kind: file                 # see the table below
    ref: modules/network/main.tf@abc1234
    retrieved: 2026-09-19
confidence: high               # derived from source.kind
parent: '[[proj/tasks/proj-142|proj-142]]'   # see Links; the project note when there is no task
repo_note: '[[proj/notes/acme-infrastructure|acme/infrastructure]]'  # when that note exists
supersedes:                    # optional: ids of entries this one replaces
    - 2026-09-18T0910-proj-142-finding-7c1e
---

for_each over a provider-versioned map breaks on upgrade; the keys are recomputed
```

Filename: `<ts>-<task>-<kind>-<4 hex>.md`. The random suffix removes the
same-second collision case for parallel hosts; a collision anyway is reported and
retried, never resolved by overwriting.

The entry's **id** is its file name without `.md`. Bundles print it under each
entry (`id 2026-09-19T1432-proj-142-finding-a3f0`), since it is what
`supersedes` takes.

### Superseding an entry

The log is append-only, so an entry that turns out wrong — a claim that no
longer holds, or a finding filed under the wrong repo name or scope — is not
edited. A new finding or decision names it in `supersedes`, and from then on:

- the old entry is left out of everything an agent reads: bundles, `find`,
  repo notes, the graph, the checks;
- the old file is untouched, and the web log view still shows it, struck
  through, with the id of its replacement.

Which entries are replaced is derived on read from the `supersedes` lists,
like dependencies — never stored on the old entry, which would mean editing
it. Only findings, decisions and notes can be replaced; `opened` and `closed`
record that something happened, and a dependency has `resolved`. The target
must be in the same project and not already replaced, so there is always one
live answer: to correct a correction, supersede the correction.

A replaced entry may already be folded into a consolidated note body. The
replacement is newer than `consolidated_through`, so it is returned beside the
body until the next consolidation rewrites it.

### Scope

The field the system turns on.

| Scope | Means | Survives the task |
|---|---|---|
| `task` | true only of this piece of work | no |
| `repo` | true about a repository, for whoever works on it next | yes |
| `project` | true about the project as a whole | yes |

A finding that would help someone who has never heard of the task is not
task-scoped. When in doubt, the wider scope is the better guess.

### Confidence

Derived from `source.kind`, because a model asked to rate its own certainty rates
it high. A caller may pass a *lower* level and it is honoured; a higher one is
ignored.

| Source kind | Derived | Why |
|---|---|---|
| `file`, `merge-request` | high | names a commit or a diff, so it can be re-checked exactly |
| `issue` | medium | a real record, but one that changes under you |
| `document` | medium | anything you can open again at a named place: a vault note, a PDF, a spec, a plan. Ranks below `file` because a plan describing code is only as good as the code it was written from |
| `confluence` | medium | a wiki page — re-readable, and it changes silently |
| `web` | low | a URL rots, paywalls, or changes with no version and no notice |
| `slack`, `conversation` | low | someone said so; unrecheckable afterwards |
| `inferred` | low | reasoned, not verified — never presented as fact |

Neither `merge-request` nor `issue` names a forge. A GitLab merge request and a
GitHub pull request are the same object — same worth, same mutability — and the
ref says which one it is, so a kind per forge would only have given an agent a
word to hesitate over. `pull-request`, `gitlab-mr`, `jira` and friends are
accepted on input and stored as the canonical kind, so a filter over the log
does not show one thing under two names.

A kind exists only where it changes one of three answers: what the claim is
worth, whether it will have moved since, and which tool re-checks it. Two
candidates that answer all three the same way are one kind — which is why an
Obsidian note and a local PDF share `document` (both are a file you open at a
named place, and the ref carries which), while `confluence` stays separate
(its own tool, and it changes under you) and `web` stays separate (it may
simply be gone).

### Staleness

Computed from dates alone, since agentctx makes no network calls:

- `source.kind: inferred` → always marked `re-verify`, at any age
- `source.retrieved` older than `stale.max_age_days` (default 30) → `re-verify`
- an unparseable `retrieved` → `re-verify`

A `file` finding returns its stored `ref`, so an agent that cares can compare the
commit with HEAD itself in one command.

## Dependency entry

An ordering between tasks, in the same append-only log:

```yaml
---
kind: dependency
task: proj-211                 # the task that waits
project: proj
scope: task
ts: 2026-09-19T15:03:03
host: kepler-claude-code
depends_on:
    - proj-210
resolved:                      # true lifts these edges instead of adding them
---

Waits for [[proj/tasks/proj-210|proj-210]].

The VPC cannot be added until the module exposes per-environment inputs
```

The current graph is folded out of these, last entry per pair winning, with
closed tasks treated as no longer blocking. Nothing stores a `blocked_by` list:
it would need clearing every time a blocker finished, and would not get it.

The `[[wikilinks]]` are what make the ordering visible in Obsidian's own graph
view without anything else being built.

## Task note

Written once at creation, and thereafter only by the consolidation pass.

```yaml
---
id: proj-142
source: jira                   # jira | gitlab | manual
source_key: PROJ-142
source_url: https://example.atlassian.net/browse/PROJ-142
title: Rework the network module
project: proj
repos:
    - acme/infrastructure
created: 2026-09-19
consolidated_through:          # log entries older than this are folded into the body
parent: '[[proj/proj|proj]]'
---

## Current understanding
## Decisions
## Findings
## Open threads
```

Absent by design:

- **`status`, `mrs`, `blocked_by`** — live state. Stored state drifts; ask the
  tracker.
- **`last_opened`, `last_host`** — derived from the `opened` entries in the log.
  Writing them back would mean every session rewriting a shared file on a synced
  vault, which is the hazard append-only exists to avoid.

Dates are emitted unquoted so Obsidian Bases treats them as dates rather than
strings.

## Project note

```yaml
---
id: proj
name: Example project
jira_projects:                 # key prefix → project mapping lives here
    - PROJ
gitlab_groups:
    - acme
repos:
    - acme/infrastructure
confluence_spaces:
    - PROJ
created: 2026-09-19
consolidated_through:
---
```

`jira_projects` and `gitlab_groups` are how `PROJ-142` resolves to this folder.
They cannot be guessed, which is why creating a project requires arguments and
why a project is never created implicitly.

## Links

The ids in `task`, `project` and `repo` are what agentctx reads. Beside them,
every file carries the same relation as a link, so Obsidian's graph and anything
indexing the vault can follow it:

| File | `parent` | `repo_note` |
|---|---|---|
| log entry with a task | its task note | its repo note, if the entry has a repo and the note exists |
| log entry without a task | the project note | as above |
| task note, repo note | the project note | — |

One parent per file, not a link to every ancestor: the project note is linked by
its tasks and its project-scoped entries, not by every entry of every task.

Links are **path-qualified**: `[[proj/tasks/proj-142|proj-142]]`, never
`[[proj-142]]`. Task ids are often tracker keys, and a vault indexed beside a
tracker export holds two files named `proj-142.md`. A bare link resolves to
whichever one a resolver prefers; a path cannot be mistaken.

A link is only written to a file that exists. A dangling link in Obsidian is one
click from creating an empty file there, and an empty file where a repo note
belongs is one consolidation cannot read.

`agentctx relink` backfills files written before links existed, and adds a
`repo_note` link to entries whose repo note appeared later. It is the one command
that edits a log entry, and it only adds links derived from ids already in the
file. It edits frontmatter as text, so properties added in Obsidian survive.
`--dry-run` lists what it would change.

## consolidated_through

Consolidation is compaction, never correctness. A bundle returns the note body
*plus* every entry newer than `consolidated_through`, so skipping consolidation
costs verbosity and never a fact. Log entries are not edited or deleted by it.
