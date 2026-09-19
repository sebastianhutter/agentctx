// Package gui is the macOS menu bar app: a place to see whether the MCP server
// is up, start and stop it, and edit the configuration.
//
// It exists because the SSE transport needs something to own the server's
// lifetime. agentctx has no interface of its own — it works when an agent calls
// it — so this deliberately shows status and does nothing clever.
package gui

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/store"
)

// App is the menu bar application.
type App struct {
	fyneApp fyne.App
	cfg     *config.Config
	cfgMu   sync.RWMutex

	configPath string
	version    string

	mcpService MCPService

	// statusLabel is the cached vault summary shown in the menu.
	statusMu    sync.Mutex
	statusLabel string

	// mcpStarting is true while the auto-start goroutine is in flight.
	mcpStarting bool

	settingsWin fyne.Window

	logPath string
	logFile *os.File

	// rebuildCh coalesces menu rebuild requests from any goroutine into the one
	// goroutine that actually calls SetSystemTrayMenu: fyne.io/systray panics
	// if ResetMenu is called concurrently.
	rebuildCh chan struct{}

	done chan struct{}
}

// Run starts the menu bar app and blocks until it quits.
func Run(configPath, version string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// A fresh log per session: this file exists to answer "why did the server
	// not start", and a growing one buries the answer.
	logPath := filepath.Join(config.DefaultConfigDir, "agentctx.log")
	var logWriter io.Writer = os.Stderr
	var logFile *os.File
	if f, err := os.Create(logPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create log file %s: %v\n", logPath, err)
	} else {
		logFile = f
		logWriter = io.MultiWriter(os.Stderr, logFile)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: slog.LevelInfo})))

	a := &App{
		cfg:         cfg,
		configPath:  configPath,
		version:     version,
		logPath:     logPath,
		logFile:     logFile,
		statusLabel: "Loading…",
		rebuildCh:   make(chan struct{}, 1),
		done:        make(chan struct{}),
		mcpService:  MCPService{configPath: configPath, version: version},
	}

	// Must happen on the main goroutine.
	a.fyneApp = app.New()
	a.fyneApp.SetIcon(trayIcon())

	a.doRebuildMenu()
	go a.menuRebuildLoop()
	go a.statusTimer()

	if cfg.GUI.AutoStartMCP {
		a.mcpStarting = true
		go func() {
			if err := a.mcpService.Start(cfg.GUI.MCPPort); err != nil {
				slog.Error("auto-start MCP failed", "err", err)
				a.notify("Could not start the MCP server: " + err.Error())
			}
			a.mcpStarting = false
			a.requestRebuild()
		}()
	}

	go a.updateStatus()

	slog.Info("agentctx menu bar app started", "version", version)
	a.fyneApp.Run()

	close(a.done)
	a.mcpService.Stop()
	slog.Info("agentctx menu bar app stopped")
	if a.logFile != nil {
		a.logFile.Close()
	}
	return nil
}

// requestRebuild asks for a menu refresh. Repeated calls coalesce.
func (a *App) requestRebuild() {
	select {
	case a.rebuildCh <- struct{}{}:
	default:
	}
}

// menuRebuildLoop serialises menu rebuilds, with a short debounce so a burst
// collapses into one.
func (a *App) menuRebuildLoop() {
	for {
		select {
		case <-a.rebuildCh:
			time.Sleep(50 * time.Millisecond)
			for {
				select {
				case <-a.rebuildCh:
				default:
					goto drained
				}
			}
		drained:
			fyne.Do(a.doRebuildMenu)
		case <-a.done:
			return
		}
	}
}

// doRebuildMenu builds and installs the tray menu. Single-goroutine only.
func (a *App) doRebuildMenu() {
	running := a.mcpService.IsRunning()

	menu := fyne.NewMenu("agentctx",
		a.mcpMenuItem(),
		&fyne.MenuItem{
			Label:    "Restart MCP Server",
			Action:   a.restartMCP,
			Disabled: a.mcpStarting || !running,
		},
		&fyne.MenuItem{
			Label:    a.endpointLabel(),
			Action:   a.copyEndpoint,
			Disabled: !running,
		},
		fyne.NewMenuItemSeparator(),
		a.statusMenuItem(),
		&fyne.MenuItem{Label: "Open Vault…", Action: a.openVault},
		fyne.NewMenuItemSeparator(),
		// Present but inert until the web interface exists. Shown rather than
		// hidden so the menu says what the app is going to be, and so its
		// absence is obviously deliberate rather than a missing feature.
		&fyne.MenuItem{Label: "Open Web Interface (not built yet)", Disabled: true},
		fyne.NewMenuItemSeparator(),
		&fyne.MenuItem{Label: "Settings…", Action: a.openSettings},
		&fyne.MenuItem{Label: "View Logs…", Action: a.openLogs},
		fyne.NewMenuItemSeparator(),
		&fyne.MenuItem{Label: "Quit", Action: func() { a.fyneApp.Quit() }},
	)

	if desk, ok := a.fyneApp.(desktop.App); ok {
		desk.SetSystemTrayMenu(menu)
	}
}

func (a *App) mcpMenuItem() *fyne.MenuItem {
	var label string
	switch {
	case a.mcpService.IsRunning():
		label = fmt.Sprintf("MCP: Running (:%d)", a.mcpService.Port())
	case a.mcpStarting:
		label = "MCP: Starting…"
	default:
		label = "MCP: Stopped"
	}
	return &fyne.MenuItem{
		Label:    label,
		Action:   a.toggleMCP,
		Disabled: a.mcpStarting,
	}
}

func (a *App) endpointLabel() string {
	if url := a.mcpService.URL(); url != "" {
		return "Copy Endpoint  " + url
	}
	return "Copy Endpoint"
}

func (a *App) statusMenuItem() *fyne.MenuItem {
	a.statusMu.Lock()
	label := a.statusLabel
	a.statusMu.Unlock()
	return &fyne.MenuItem{Label: label, Disabled: true}
}

func (a *App) toggleMCP() {
	if a.mcpService.IsRunning() {
		a.mcpService.Stop()
		a.requestRebuild()
		return
	}

	a.cfgMu.RLock()
	port := a.cfg.GUI.MCPPort
	a.cfgMu.RUnlock()

	if err := a.mcpService.Start(port); err != nil {
		slog.Error("failed to start MCP", "err", err)
		a.notify("Could not start the MCP server: " + err.Error())
	}
	a.requestRebuild()
}

func (a *App) restartMCP() {
	a.cfgMu.RLock()
	port := a.cfg.GUI.MCPPort
	a.cfgMu.RUnlock()

	if err := a.mcpService.Restart(port); err != nil {
		slog.Error("failed to restart MCP", "err", err)
		a.notify("Could not restart the MCP server: " + err.Error())
	}
	a.requestRebuild()
}

// copyEndpoint puts the SSE URL on the clipboard, which is the one piece of
// text needed to register this server with a client.
func (a *App) copyEndpoint() {
	url := a.mcpService.URL()
	if url == "" {
		return
	}
	a.fyneApp.Clipboard().SetContent(url)
	a.notify("Endpoint copied: " + url)
}

// openVault reveals the vault in Finder — the answer to "where did that
// decision actually go".
func (a *App) openVault() {
	a.cfgMu.RLock()
	path := a.cfg.ExpandedVaultPath()
	a.cfgMu.RUnlock()

	if path == "" {
		a.notify("No vault configured yet — open Settings and choose one.")
		return
	}
	if err := exec.Command("open", path).Start(); err != nil {
		slog.Error("failed to open vault", "path", path, "err", err)
	}
}

func (a *App) openLogs() {
	if _, err := os.Stat(a.logPath); os.IsNotExist(err) {
		if f, err := os.Create(a.logPath); err == nil {
			f.Close()
		}
	}
	if err := exec.Command("open", a.logPath).Start(); err != nil {
		slog.Error("failed to open log file", "path", a.logPath, "err", err)
	}
}

// statusTimer refreshes the vault summary periodically. A minute is plenty:
// nothing here is time-critical, and a directory walk on a synced folder is not
// free.
func (a *App) statusTimer() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.updateStatus()
		case <-a.done:
			return
		}
	}
}

// updateStatus counts what is in the vault, so the menu shows whether anything
// is actually being written — the only number that says the system is working.
func (a *App) updateStatus() {
	a.cfgMu.RLock()
	cfg := a.cfg
	a.cfgMu.RUnlock()

	label := "No vault configured"
	if problems := cfg.Validate(); len(problems) == 0 {
		s := store.New(cfg.ExpandedVaultPath())
		projects, err := s.ListProjects()
		if err != nil {
			label = "Vault unreadable"
		} else {
			entries, tasks := 0, 0
			for _, p := range projects {
				if list, err := s.Entries(p, store.EntryFilter{}); err == nil {
					entries += len(list)
				}
				if list, err := s.ListTasks(p); err == nil {
					tasks += len(list)
				}
			}
			label = fmt.Sprintf("%s, %s, %s", plural(len(projects), "project"),
				plural(tasks, "task"), plural(entries, "entry", "entries"))
		}
	}

	a.statusMu.Lock()
	a.statusLabel = label
	a.statusMu.Unlock()
	a.requestRebuild()
}

// ReloadConfig re-reads configuration after the settings dialog saves.
func (a *App) ReloadConfig() {
	cfg, err := config.Load(a.configPath)
	if err != nil {
		slog.Error("reload config failed", "err", err)
		return
	}
	a.cfgMu.Lock()
	a.cfg = cfg
	a.cfgMu.Unlock()

	go a.updateStatus()
	a.requestRebuild()
}

func (a *App) notify(message string) {
	a.fyneApp.SendNotification(fyne.NewNotification("agentctx", message))
}

// plural renders "1 project" / "2 projects", taking an explicit plural for the
// words English declines to make regular.
func plural(n int, singular string, plural ...string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	if len(plural) > 0 {
		return fmt.Sprintf("%d %s", n, plural[0])
	}
	return fmt.Sprintf("%d %ss", n, singular)
}
