# CLAUDE.md

## Project: agentctx

A durable, agent-writable knowledge layer over a dedicated Obsidian vault. Agents
log **decisions** and **findings** during the work, each with provenance; entries
are append-only markdown files. Findings are scoped `task`, `repo` or `project`,
and the repo- and project-scoped ones are the point — they outlive the task that
discovered them.

Not a task tracker, not an aggregator, not an integration hub.

The full design and build order live in `docs/implementation-plan.md`. Read it
before writing code.

---

## Rule 0 — this repository names no customer, project or person

**The repository is private today and may be made public later. Git history is
permanent: scrubbing a name before flipping visibility does not remove it.**
Therefore the rule applies from the first commit, not from the day it goes public.

Never write into code, tests, fixtures, doc comments, `docs/`, `configs/`,
`README.md`, branch names or commit messages:

- customer, employer or client names
- project, product or Jira project names, and real issue keys
- repository or GitLab group names from real work
- people's names, usernames, or mail addresses
- internal hostnames, Jira/Confluence/GitLab site URLs, VPN or network names
- real note titles, file paths or folder names from the vault

The temptation is specific to this project: developing it means running it against
a real vault holding real work, so the obvious example to reach for when writing a
comment, a test fixture or a doc is the one just in front of you. **Translate
before writing it down.**

Use these placeholders consistently:

| Real thing | Write instead |
|---|---|
| project / Jira project | `proj`, `PROJ` |
| issue key | `PROJ-142` |
| a key that must *not* resolve, in a test | `OTHER-7`, `NOPE-1` |
| tracker site | `example.atlassian.net`, `gitlab.example.com` |
| org / GitLab group | `acme` |
| repositories | `acme/infrastructure`, `acme/platform` |
| a person | `someone`, or omit |

`scripts/check-names.sh` enforces the key prefixes against a closed list
(`PROJ`, `OTHER`, `NOPE`, `ACME`). Adding a prefix to it is a decision to record
here, not a reflex to silence a failing check.

Aggregate measurements are welcome and are what makes documentation useful — "a
project accumulated 40 findings over two weeks" is fine. The identities behind
them are not.

One exemption, and only one: the repository's own address — the Go module path,
the remote URL, the `LICENSE` copyright line. That is the project's identity, not
corpus data, and it is public the moment the repository is.

Applies equally to anything written **about** the vault. The real vault path
belongs in `~/.agentctx/config.json` on the machine, never in a committed file;
`configs/config.example.json` carries a neutral example path.

The one thing this rule does *not* cover: the vault contents themselves. Those are
outside the repository and are supposed to hold real names — that is the point of
the tool. Never copy a vault entry into the repo as a fixture; write a synthetic
one.

---

## Key constraints

- **No external interfaces.** agentctx makes no network calls and holds no
  credentials: no Jira client, no GitLab client, no `local-rag` client. Facts enter
  through required tool fields, supplied by an agent that already has those MCPs.
  If a feature seems to need an interface, it is a v2 discussion, not a patch.
- **Append-only.** Agents never edit an existing file; they append a new one. The
  vault is on a file-sync service with last-write-wins and no locking, and parallel
  sessions are normal. Two writers must produce two files, never one corruption.
- **The log is the source of truth.** Note bodies are consolidated views, rewritten
  only by the consolidation pass. Log entries are never edited or deleted.
- **Nothing enters without provenance.** Every finding records where it came from
  and when. No wholesale copying from a tracker, a wiki or a search result.
- **Projects are created deliberately, tasks lazily.** Never auto-create a project.
- **Never infer identity from the working directory.** See the resolution chain in
  the plan; a wrong guess attaches entries to the wrong task silently.
- **stdout belongs to the MCP protocol.** All logging goes to stderr via
  `log/slog`. A stray `fmt.Println` corrupts a stdio session.

---

## Coding standards

Inherited from `local-rag`, so both repositories read the same way:

- Exported types and functions have Go doc comments
- Structs for structured data — no untyped maps in a public API
- Errors returned and wrapped with `fmt.Errorf("...: %w", err)`, not panics
- No global state — pass `*config.Config` explicitly through the call stack
- `log/slog` for logging, never `fmt.Print`
- Tests live in `_test.go` files beside the code they test, and run against a
  temp-dir vault — never against the real one
- Pure Go, no CGO. `make build`, `make test`, `make lint`
