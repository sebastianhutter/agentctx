package gui

import (
	"fmt"
	"log/slog"
	"strconv"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/sebastianhutter/agentctx/internal/config"
)

// openSettings shows the settings window.
//
// Small on purpose. agentctx has five settings that matter, and a configuration
// screen that mirrors every field in the JSON would be a worse way to edit the
// JSON. Anything not here is a deliberate default; the file is still there for
// anyone who disagrees.
func (a *App) openSettings() {
	if a.settingsWin != nil {
		a.settingsWin.Show()
		a.settingsWin.RequestFocus()
		return
	}

	w := a.fyneApp.NewWindow("agentctx Settings")
	w.Resize(fyne.NewSize(620, 420))
	a.settingsWin = w

	a.cfgMu.RLock()
	// A copy, so Cancel discards.
	cfg := *a.cfg
	a.cfgMu.RUnlock()

	tabs := container.NewAppTabs(
		container.NewTabItem("Vault", a.buildVaultTab(&cfg, w)),
		container.NewTabItem("Server", a.buildServerTab(&cfg)),
	)

	var saveBtn *widget.Button
	saveBtn = widget.NewButton("Save", func() {
		saveBtn.Disable()
		defer saveBtn.Enable()

		if problems := cfg.Validate(); len(problems) > 0 {
			// Refuse rather than write a configuration that leaves the server
			// running against nothing.
			dialog.ShowError(joinErrors(problems), w)
			return
		}
		if err := config.Save(&cfg, a.configPath); err != nil {
			slog.Error("save config failed", "err", err)
			dialog.ShowError(err, w)
			return
		}
		if err := SetStartOnLogin(cfg.GUI.StartOnLogin); err != nil {
			slog.Error("set start-on-login failed", "err", err)
			dialog.ShowError(fmt.Errorf("saved, but start-on-login could not be set: %w", err), w)
		}

		a.ReloadConfig()

		// The port is read when the server starts, so a changed one only takes
		// effect on restart. Offering that here beats leaving the user to
		// wonder why the endpoint did not move.
		if a.mcpService.IsRunning() && a.mcpService.Port() != cfg.GUI.MCPPort {
			dialog.ShowConfirm("Restart the MCP server?",
				fmt.Sprintf("The port changed to %d. The server is still listening on %d until it restarts.",
					cfg.GUI.MCPPort, a.mcpService.Port()),
				func(yes bool) {
					if yes {
						a.restartMCP()
					}
					w.Close()
				}, w)
			return
		}
		w.Close()
	})
	saveBtn.Importance = widget.HighImportance

	buttons := container.NewHBox(
		widget.NewButton("Cancel", func() { w.Close() }),
		saveBtn,
	)

	w.SetContent(container.NewBorder(nil, container.NewPadded(buttons), nil, nil, tabs))
	w.SetOnClosed(func() { a.settingsWin = nil })
	w.Show()
}

func (a *App) buildVaultTab(cfg *config.Config, w fyne.Window) fyne.CanvasObject {
	vaultEntry := widget.NewEntry()
	vaultEntry.SetText(config.UnexpandPath(cfg.VaultPath))
	vaultEntry.OnChanged = func(s string) { cfg.VaultPath = s }

	browse := widget.NewButton("Choose…", func() {
		dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
			if err != nil || uri == nil {
				return
			}
			vaultEntry.SetText(config.UnexpandPath(uri.Path()))
		}, w)
	})

	staleEntry := widget.NewEntry()
	staleEntry.SetText(strconv.Itoa(cfg.Stale.MaxAgeDays))
	staleEntry.Validator = intValidator(0, 3650)
	staleEntry.OnChanged = func(s string) {
		if v, err := strconv.Atoi(s); err == nil {
			cfg.Stale.MaxAgeDays = v
		}
	}

	hostEntry := widget.NewEntry()
	hostEntry.SetText(cfg.Host)
	hostEntry.SetPlaceHolder("detected automatically — leave empty")
	hostEntry.OnChanged = func(s string) { cfg.Host = s }

	form := widget.NewForm(
		widget.NewFormItem("Vault", container.NewBorder(nil, nil, nil, browse, vaultEntry)),
		widget.NewFormItem("Re-verify after", staleEntry),
		widget.NewFormItem("Host name", hostEntry),
	)

	notes := widget.NewRichTextFromMarkdown(
		"**Vault** — the dedicated agentctx vault, not your main Obsidian vault. " +
			"Everything under it is written by an agent.\n\n" +
			"**Re-verify after** — days before a finding is returned marked for re-checking. " +
			"Findings recorded as inferred are always marked, whatever their age.\n\n" +
			"**Host name** — stamped on every entry, to show later which hosts actually wrote " +
			"anything. Detected automatically over stdio; set it here for hosts that cannot be told apart.")
	notes.Wrapping = fyne.TextWrapWord

	return container.NewBorder(container.NewPadded(form), nil, nil, nil, container.NewPadded(notes))
}

func (a *App) buildServerTab(cfg *config.Config) fyne.CanvasObject {
	autoStart := widget.NewCheck("Start the MCP server when this app launches", func(b bool) {
		cfg.GUI.AutoStartMCP = b
	})
	autoStart.SetChecked(cfg.GUI.AutoStartMCP)

	startOnLogin := widget.NewCheck("Launch agentctx at login", func(b bool) {
		cfg.GUI.StartOnLogin = b
	})
	startOnLogin.SetChecked(cfg.GUI.StartOnLogin)

	portEntry := widget.NewEntry()
	portEntry.SetText(strconv.Itoa(cfg.GUI.MCPPort))
	portEntry.Validator = intValidator(1024, 65535)
	portEntry.OnChanged = func(s string) {
		if v, err := strconv.Atoi(s); err == nil {
			cfg.GUI.MCPPort = v
		}
	}

	form := widget.NewForm(
		widget.NewFormItem("Port", portEntry),
		widget.NewFormItem("", autoStart),
		widget.NewFormItem("", startOnLogin),
	)

	notes := widget.NewRichTextFromMarkdown(
		"Register the running server with a client at `http://localhost:" +
			strconv.Itoa(cfg.GUI.MCPPort) + "/sse` — *Copy Endpoint* in the menu puts it on the clipboard.\n\n" +
			"**One server, many clients.** Over this connection agentctx cannot see any caller's " +
			"working directory or environment, so `open_task` must be given a `task_id` and cannot " +
			"infer one. A host that supports a stdio registration can infer it, and is worth preferring " +
			"where you have the choice:\n\n" +
			"    agentctx serve")
	notes.Wrapping = fyne.TextWrapWord

	return container.NewBorder(container.NewPadded(form), nil, nil, nil, container.NewPadded(notes))
}

// intValidator rejects anything that is not a number in range.
func intValidator(min, max int) fyne.StringValidator {
	return func(s string) error {
		v, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("must be a number")
		}
		if v < min || v > max {
			return fmt.Errorf("must be between %d and %d", min, max)
		}
		return nil
	}
}

// joinErrors renders configuration problems as one error for a dialog.
func joinErrors(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	msg := "several settings are not usable:"
	for _, e := range errs {
		msg += "\n• " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
