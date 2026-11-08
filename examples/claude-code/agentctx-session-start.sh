#!/usr/bin/env bash
#
# SessionStart hook for Claude Code: tell the session what agentctx already
# knows about this repository, if anything.
#
# Install: copy to ~/.claude/hooks/, make it executable, and register it in
# ~/.claude/settings.json (see README.md, "Making agents use it").
#
# Two rules for a hook that runs in every session on the machine:
#
#  1. Silent unless relevant. A repository agentctx has never heard of gets no
#     output at all — otherwise it is noise in most sessions and gets deleted.
#  2. Never break a session. Any failure exits 0 with no output. A missing
#     binary, an unconfigured vault or a directory that is not a repository
#     must cost nothing.
#
# It deliberately does not open a task. Which task is being worked on is an
# argument the agent passes, never a guess from a directory — a wrong guess
# files work under the wrong task and nobody notices. So this offers
# orientation and lets the session choose.

set -uo pipefail

find_agentctx() {
    for candidate in \
        "/Applications/agentctx.app/Contents/MacOS/agentctx" \
        "$HOME/.local/bin/agentctx" \
        "$(command -v agentctx 2>/dev/null)"
    do
        [ -n "$candidate" ] && [ -x "$candidate" ] && { printf '%s' "$candidate"; return 0; }
    done
    return 1
}

BIN=$(find_agentctx) || exit 0

# An unusable configuration is not this hook's problem to report.
"$BIN" doctor >/dev/null 2>&1 || exit 0

# owner/repo from the origin remote, which is how repositories are named in
# the vault.
remote=$(git remote get-url origin 2>/dev/null) || exit 0
repo=$(printf '%s' "$remote" \
    | sed -E 's#^git@[^:]+:##; s#^https?://[^/]+/##; s#\.git$##')
[ -n "$repo" ] || exit 0

found=$("$BIN" find --repo "$repo" --limit 8 2>/dev/null) || exit 0

# Nothing recorded for this repository: say nothing.
case "$found" in
    ""|*"Nothing here matches"*) exit 0 ;;
esac

cat <<EOF
## agentctx

This repository appears in your agentctx vault. What is recorded:

$found

Before reading code, call \`open_task\` with the task id for the work at hand —
it returns what is already known about the task, its project and its
repositories. If none of the above is this work, call \`find\` to look, or
\`open_task\` with a seed to start a new one. Log decisions and findings as you
make them, not at the end.
EOF
