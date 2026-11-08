# CLAUDE.md

## Project: agentctx

A durable, agent-writable knowledge layer over a dedicated Obsidian vault. Agents
log **decisions** and **findings** during the work, each with provenance; entries
are append-only markdown files. Findings are scoped `task`, `repo`,
`environment`, `service` or `project`, and all but the task-scoped ones are the
point — they outlive the task that discovered them.

Not a task tracker, not an aggregator, not an integration hub.

The full design and build order live in `docs/implementation-plan.md`. Read it
before writing code.

---

## Rule 0 — this repository names no customer, project or person

**The repository is public. Git history is permanent: a name removed in a later
commit is still readable by anyone.** The rule has applied from the first commit,
and the history was checked against it before visibility was flipped.

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
| a module or file path | `modules/network/main.tf` |
| a person | `someone`, or omit |

Nothing enforces this automatically. It is a rule you and any agent working here
follow while writing, which means the moment to apply it is **before** the text
exists, not in a review pass afterwards.

**Before pushing**, grep what you are about to push — the diff and the commit
messages — for the names you know are yours: your employer, your customers, their
products, their issue-key prefixes, their tracker hostnames. Once a name is on
the public remote, rewriting history no longer recalls it: forks, clones and
caches already have it.

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
- **Derived state is never stored.** Open/closed, and which tasks block which,
  are folded out of the log on read. A stored copy needs clearing when reality
  changes and will not get it — that is why `status` and `blocked_by` are not
  fields.
- **The log is the source of truth.** Note bodies are consolidated views, written
  only by `consolidate` — the single exception to append-only, and the only place
  two writers could lose each other's work. Log entries are never edited or
  deleted.
- **Nothing enters without provenance.** Every finding records where it came from
  and when. No wholesale copying from a tracker, a wiki or a search result.
- **Projects are created deliberately, tasks lazily.** Never auto-create a project.
  What belongs to one changes through `amend` entries folded by `ReadProject`,
  never by rewriting the note's frontmatter — consolidation reads the raw note
  (`readProjectNote`) so the folded lists are never stored.
- **`find` returns identifiers, never content.** It is how a caller gets an id to
  pass to something else. The moment it starts returning findings it becomes a
  worse copy of a retrieval tool that already indexes this vault.
- **The task is an argument, never a guess.** `task_id` is required on every verb
  that writes. Do not reintroduce inference from the working directory, a state
  file or a branch name: a wrong guess attaches entries to the wrong task
  silently, and that is the one mistake here that cannot be detected or repaired.
  See §10 of the implementation plan.
- **The web interface is read-mostly and localhost-only.** One write action —
  creating a project. No editing note bodies: Obsidian does that better, and
  everything an agent writes goes through the verbs. Keep the `Host` check: a
  loopback bind alone does not stop DNS rebinding.
- **Ids become path segments.** Anything that turns one into a path goes through
  `store.ValidateID`. Unchecked, `../../etc` reaches outside the vault — which
  was theoretical until ids started arriving from URLs.
- **stdout belongs to the MCP protocol.** All logging goes to stderr via
  `log/slog`. A stray `fmt.Println` corrupts a stdio session.
- **The environment describes the caller only over stdio and the CLI.** The SSE
  server is shared, so its environment is the menu bar app's. Host and session
  attribution therefore go through `service.Transport`; nothing else may read the
  environment to decide something about the caller.

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
- macOS only, CGO enabled (the menu bar app is Fyne). `make build`, `make test`,
  `make app`, `make dmg`
