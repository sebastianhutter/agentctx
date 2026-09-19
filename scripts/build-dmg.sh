#!/usr/bin/env bash
#
# build-dmg.sh — Create a DMG installer for agentctx.
#
# Expects bin/agentctx.app to exist (run `make app` first). Produces
# bin/agentctx.dmg with the app and an Applications symlink, so installing is
# the usual drag.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$PROJECT_DIR/bin"
APP_DIR="$BUILD_DIR/agentctx.app"
DMG_PATH="$BUILD_DIR/agentctx.dmg"
VOLUME_NAME="agentctx"

if [[ ! -d "$APP_DIR" ]]; then
    echo "Error: .app bundle not found at $APP_DIR — run 'make app' first." >&2
    exit 1
fi

rm -f "$DMG_PATH"

STAGING=$(mktemp -d)
trap 'rm -rf "$STAGING"' EXIT

cp -R "$APP_DIR" "$STAGING/"
ln -s /Applications "$STAGING/Applications"

# A README on the volume, because two things about this app are not guessable:
# a download is quarantined until it is opened once from the context menu, and
# the binary a host should be told to run is inside the bundle.
cat > "$STAGING/Read me first.txt" <<'README'
agentctx
========

1. Drag agentctx.app to the Applications folder.

2. The first launch must be from the context menu: right-click (or
   Control-click) the app and choose Open, then confirm. macOS quarantines
   anything downloaded from the internet that is not notarised, and a normal
   double-click is refused without offering a way through.

   The equivalent from a terminal:

       xattr -d com.apple.quarantine /Applications/agentctx.app

3. agentctx runs in the menu bar. Its menu shows whether the MCP server is
   running, starts and stops it, and copies the endpoint to register with a
   client — by default:

       http://localhost:31124/sse

4. Configuration lives in ~/.agentctx/config.json and needs at least the path
   to your agentctx vault:

       {
         "vault_path": "~/Obsidian/agentctx"
       }

5. The same binary is the command-line tool, inside the bundle:

       /Applications/agentctx.app/Contents/MacOS/agentctx

   To use it as a command, link it onto your PATH:

       mkdir -p ~/.local/bin
       ln -sf /Applications/agentctx.app/Contents/MacOS/agentctx ~/.local/bin/agentctx

6. A host that can spawn its own server should be registered for stdio instead
   of the shared SSE endpoint:

       agentctx serve

   A stdio server sees the agent's working directory and environment, so it can
   work out which task is meant. The shared SSE server cannot, and needs a
   task_id on every call.
README

# Build in two steps — create an empty read/write image, copy into it, then
# convert — rather than handing hdiutil the staging folder with -srcfolder.
#
# -srcfolder is one command and works on most machines, but it fails with
# "Resource busy" when the folder holds a Mach-O binary that macOS decides to
# scan as it is read. Measured while building this: a folder holding 25MB of
# random bytes images fine, the same bytes as an executable do not. The two-step
# form does the copy with cp, where a scan is something the filesystem waits
# for rather than something hdiutil trips over.
#
# Size the intermediate generously: it is a scratch volume, the final UDZO image
# is compressed, and under-provisioning fails late with "No space left on
# device" after the copy has already run.
STAGING_MB=$(du -sm "$STAGING" | cut -f1)
SIZE_MB=$(( STAGING_MB + 100 ))

RW_DMG="$(mktemp -d)/rw.dmg"
hdiutil create -size "${SIZE_MB}m" -fs HFS+ -volname "$VOLUME_NAME" -ov "$RW_DMG" >/dev/null

MOUNT_POINT=$(hdiutil attach "$RW_DMG" -nobrowse -noautoopen | grep -o '/Volumes/.*$' | tail -1)
if [[ -z "$MOUNT_POINT" ]]; then
    echo "Error: could not mount the scratch image." >&2
    exit 1
fi

# From here on the volume must be detached whatever happens, or it is left
# mounted and the next build collides with it.
cleanup_mount() {
    hdiutil detach "$MOUNT_POINT" -force >/dev/null 2>&1 || true
}
trap 'cleanup_mount; rm -rf "$STAGING" "$(dirname "$RW_DMG")"' EXIT

cp -R "$STAGING/." "$MOUNT_POINT/"

# Detaching straight after a copy can report "Resource busy" while macOS is
# still reading what was written. Retry, then force — the alternative is a
# stuck volume and a failed build for a reason that resolves itself in a second.
detached=false
for attempt in 1 2 3 4 5; do
    if hdiutil detach "$MOUNT_POINT" >/dev/null 2>&1; then
        detached=true
        break
    fi
    sleep 1
done
if [[ "$detached" != true ]]; then
    hdiutil detach "$MOUNT_POINT" -force >/dev/null
fi
trap 'rm -rf "$STAGING" "$(dirname "$RW_DMG")"' EXIT

hdiutil convert "$RW_DMG" -format UDZO -o "$DMG_PATH" >/dev/null

echo "Created $DMG_PATH"
