package gui

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

const (
	launchdLabel = "com.sebastianhutter.agentctx"
	plistName    = launchdLabel + ".plist"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", plistName)
}

// SetStartOnLogin writes or removes the launchd agent that starts the menu bar
// app at login.
func SetStartOnLogin(enabled bool) error {
	path := plistPath()

	if !enabled {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove plist: %w", err)
		}
		slog.Info("removed start-on-login agent", "path", path)
		return nil
	}

	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	// Deliberately not resolving symlinks: inside an .app bundle the executable
	// is the real path already, and if someone runs a symlinked binary they
	// mean the symlink, which survives a rebuild.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}

	// KeepAlive is false on purpose: this is a menu bar app, and quitting it
	// from its own menu should not have launchd start it straight back up.
	contents := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + launchdLabel + `</string>
	<key>ProgramArguments</key>
	<array>
		<string>` + escapeXML(exe) + `</string>
		<string>gui</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
</dict>
</plist>
`

	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write plist: %w", err)
	}

	slog.Info("wrote start-on-login agent", "path", path, "exe", exe)
	return nil
}

// IsStartOnLoginInstalled reports whether the launchd agent exists.
func IsStartOnLoginInstalled() bool {
	_, err := os.Stat(plistPath())
	return err == nil
}

// escapeXML escapes the characters that would break the plist. A path can
// legally contain an ampersand, and an unescaped one makes the file unparseable
// — which launchd reports as nothing happening at all.
func escapeXML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(s)
}
