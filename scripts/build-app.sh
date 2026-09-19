#!/usr/bin/env bash
#
# build-app.sh — Create a macOS .app bundle for agentctx.
#
# Expects bin/agentctx to exist (run `make build` first). Produces
# bin/agentctx.app: a menu bar app that runs the MCP server over SSE and shows
# whether it is up.
#
# Launching the bundle runs the binary with no arguments, which starts the menu
# bar app. The same binary is the command-line tool, at
# Contents/MacOS/agentctx — which is also what to register for a stdio server.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BUILD_DIR="$PROJECT_DIR/bin"
APP_NAME="agentctx"
APP_DIR="$BUILD_DIR/$APP_NAME.app"

BUNDLE_ID="com.sebastianhutter.agentctx"
VERSION="${VERSION:-0.1.0}"

BINARY="$BUILD_DIR/$APP_NAME"
if [[ ! -f "$BINARY" ]]; then
    echo "Error: binary not found at $BINARY — run 'make build' first." >&2
    exit 1
fi

ICON_SRC="$PROJECT_DIR/Icon.png"
if [[ ! -f "$ICON_SRC" ]]; then
    echo "Error: Icon.png not found at $ICON_SRC" >&2
    exit 1
fi

# ── Structure ───────────────────────────────────────────────────────────────
rm -rf "$APP_DIR"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"

cp "$BINARY" "$APP_DIR/Contents/MacOS/$APP_NAME"
chmod +x "$APP_DIR/Contents/MacOS/$APP_NAME"

# ── Icon ────────────────────────────────────────────────────────────────────
ICONSET_PARENT=$(mktemp -d)
ICONSET_DIR="$ICONSET_PARENT/$APP_NAME.iconset"
mkdir -p "$ICONSET_DIR"

# The source is 1024x1024, so every size including 512@2x is a real rendering
# rather than an upscale.
sips -z 16 16     "$ICON_SRC" --out "$ICONSET_DIR/icon_16x16.png"      >/dev/null
sips -z 32 32     "$ICON_SRC" --out "$ICONSET_DIR/icon_16x16@2x.png"   >/dev/null
sips -z 32 32     "$ICON_SRC" --out "$ICONSET_DIR/icon_32x32.png"      >/dev/null
sips -z 64 64     "$ICON_SRC" --out "$ICONSET_DIR/icon_32x32@2x.png"   >/dev/null
sips -z 128 128   "$ICON_SRC" --out "$ICONSET_DIR/icon_128x128.png"    >/dev/null
sips -z 256 256   "$ICON_SRC" --out "$ICONSET_DIR/icon_128x128@2x.png" >/dev/null
sips -z 256 256   "$ICON_SRC" --out "$ICONSET_DIR/icon_256x256.png"    >/dev/null
sips -z 512 512   "$ICON_SRC" --out "$ICONSET_DIR/icon_256x256@2x.png" >/dev/null
sips -z 512 512   "$ICON_SRC" --out "$ICONSET_DIR/icon_512x512.png"    >/dev/null
sips -z 1024 1024 "$ICON_SRC" --out "$ICONSET_DIR/icon_512x512@2x.png" >/dev/null

iconutil -c icns "$ICONSET_DIR" -o "$APP_DIR/Contents/Resources/$APP_NAME.icns"
rm -rf "$ICONSET_PARENT"

# ── Info.plist ──────────────────────────────────────────────────────────────
# LSUIElement: a menu bar app has no Dock icon and no main window.
cat > "$APP_DIR/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key>
    <string>${APP_NAME}</string>
    <key>CFBundleDisplayName</key>
    <string>agentctx</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_ID}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleExecutable</key>
    <string>${APP_NAME}</string>
    <key>CFBundleIconFile</key>
    <string>${APP_NAME}</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
</dict>
</plist>
PLIST

# Ad-hoc signature. Not notarisation and not a substitute for it: it only keeps
# macOS from refusing to run an unsigned universal binary outright. A download
# still arrives quarantined — see the README.
codesign --force --deep --sign - "$APP_DIR" 2>/dev/null || \
    echo "Note: ad-hoc codesign failed; the bundle still runs locally." >&2

echo "Created $APP_DIR"
