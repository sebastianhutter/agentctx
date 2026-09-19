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
    kind: file                 # file | gitlab-mr | jira | confluence | slack | conversation | inferred
    ref: modules/network/main.tf@abc1234
    retrieved: 2026-09-19
confidence: high               # derived from source.kind
---

for_each over a provider-versioned map breaks on upgrade; the keys are recomputed
```

Filename: `<ts>-<task>-<kind>-<4 hex>.md`. The random suffix removes the
same-second collision case for parallel hosts; a collision anyway is reported and
retried, never resolved by overwriting.

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
| `file`, `gitlab-mr` | high | names a commit or a diff, so it can be re-checked exactly |
| `jira`, `confluence` | medium | a real document, but one that changes under you |
| `slack`, `conversation` | low | someone said so |
| `inferred` | low | reasoned, not verified — never presented as fact |

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

Waits for [[proj-210]].

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

## consolidated_through

Consolidation is compaction, never correctness. A bundle returns the note body
*plus* every entry newer than `consolidated_through`, so skipping consolidation
costs verbosity and never a fact. Log entries are not edited or deleted by it.
