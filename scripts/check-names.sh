#!/usr/bin/env bash
#
# check-names.sh — enforce Rule 0 (see CLAUDE.md): this repository names no
# customer, project or person.
#
# Two layers, because a list of the names you are forbidding is itself the leak:
#
#   1. Structural patterns, committed. They catch the *shape* of a leak — a real
#      issue key, a tracker hostname, a mail address, a home directory — without
#      naming anything. This is what runs in CI and for anyone who clones.
#   2. A private literal deny-list, never committed. One name per line at
#      $AGENTCTX_DENY_LIST or ~/.agentctx/deny-names.txt. This is where the
#      actual customer and project names go, on the machine that knows them.
#
# Scans tracked files only: untracked scratch is the author's business, and
# anything not tracked cannot leak.

set -uo pipefail

fail=0
files=$(git ls-files)
[ -z "$files" ] && { echo "check-names: no tracked files yet"; exit 0; }

report() { # <label> <matches>
    printf '\n  %s\n' "$1"
    printf '%s\n' "$2" | sed 's/^/    /'
    fail=1
}

scan() { # <label> <extended-regex> [exclude-regex]
    local label="$1" pattern="$2" exclude="${3:-}" hits scanned
    # This script defines the patterns, so it matches all of them. Excluding it
    # is not a loophole: it contains regexes, not names.
    scanned=$(printf '%s\n' "$files" | grep -v '^scripts/check-names.sh$')
    hits=$(printf '%s\n' "$scanned" | xargs grep -n -E -I "$pattern" 2>/dev/null || true)
    [ -n "$exclude" ] && hits=$(printf '%s\n' "$hits" | grep -v -E "$exclude" || true)
    hits=$(printf '%s\n' "$hits" | grep -v '^[[:space:]]*$' || true)
    [ -n "$hits" ] && report "$label" "$hits"
}

# An issue key whose prefix is not one of the documented placeholders. The
# allowed set is deliberately closed and small: "add another prefix" should be a
# decision someone makes in CLAUDE.md, not a reflex to silence this check.
scan "Issue key (use PROJ-142):" \
     '\b[A-Z][A-Z0-9]{1,9}-[0-9]+\b' \
     '\b(PROJ|OTHER|NOPE|ACME|TODO|FIXME|NOTE|RFC|ISO|UTF|SHA|API|JSON|HTTP|MCP|CLI)-'

# Tracker and wiki hostnames. example.* is allowed.
scan "Tracker hostname (use example.atlassian.net / gitlab.example.com):" \
     '[A-Za-z0-9-]+\.(atlassian\.net|atlassian\.com)|gitlab\.[A-Za-z0-9.-]+' \
     'example\.atlassian\.net|gitlab\.example\.com|atlassian\.com/software'

# Mail addresses, except the placeholder domain.
scan "Mail address:" \
     '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' \
     '@example\.(com|net|org)|noreply@anthropic\.com'

# An absolute home directory names the machine's user.
scan "Absolute home path (use ~/ instead):" \
     '/(Users|home)/[A-Za-z0-9._-]+/' \
     '/Users/<|/home/<'

# The private literal list, if present.
deny="${AGENTCTX_DENY_LIST:-$HOME/.agentctx/deny-names.txt}"
if [ -f "$deny" ]; then
    while IFS= read -r name; do
        case "$name" in ''|'#'*) continue ;; esac
        hits=$(printf '%s\n' "$files" | xargs grep -n -i -I -F "$name" 2>/dev/null || true)
        [ -n "$hits" ] && report "Denied name \"$name\":" "$hits"
    done < "$deny"
else
    echo "check-names: no private deny-list at $deny (structural checks only)"
fi

if [ "$fail" -ne 0 ]; then
    cat >&2 <<'MSG'

check-names: FAILED — see Rule 0 in CLAUDE.md.

Git history is permanent. Fix this before committing, not before going public.
Placeholders: proj / PROJ-142 / acme/infrastructure / example.atlassian.net.
MSG
    exit 1
fi

echo "check-names: clean"
