//go:build windows

package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/lxn/walk"

	"limber/scheduler"
)

func (a *App) installTray() error {
	active, err := walk.NewIconFromImageForDPI(ActiveTrayIcon(), 96)
	if err != nil {
		return fmt.Errorf("active icon: %w", err)
	}
	paused, err := walk.NewIconFromImageForDPI(PausedTrayIcon(), 96)
	if err != nil {
		return fmt.Errorf("paused icon: %w", err)
	}
	a.activeIcon = active
	a.pausedIcon = paused

	ni, err := walk.NewNotifyIcon(a.mw)
	if err != nil {
		return fmt.Errorf("notify icon: %w", err)
	}
	a.ni = ni

	if err := ni.SetIcon(active); err != nil {
		return err
	}
	if err := ni.SetToolTip("Limber"); err != nil {
		return err
	}

	if err := a.buildMenu(); err != nil {
		return err
	}

	if err := ni.SetVisible(true); err != nil {
		return err
	}
	go a.runTooltipUpdater()
	return nil
}

// runTooltipUpdater refreshes the tray tooltip once per second so it shows
// the live remaining time to the next break and total active time since the
// last full break completed.
func (a *App) runTooltipUpdater() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	a.mw.Synchronize(a.refreshTooltip)
	for range t.C {
		if a.exiting.Load() {
			return
		}
		a.mw.Synchronize(a.refreshTooltip)
	}
}

func (a *App) refreshTooltip() {
	if a.ni == nil || a.exiting.Load() {
		return
	}
	_ = a.ni.SetToolTip(formatTooltip(a.Sched.Snapshot(), a.paused))
}

// formatTooltip produces e.g. "0:14 to short break - Since last full: 3:47 -
// Total working time: 03:45". When the user has snoozed the active popup, the
// first segment switches to "<type> snoozed: <elapsed>/<total>sec" so they
// can see how long until the snoozed break re-fires. Outside working hours
// most counters are held at zero so the message collapses to a status line +
// the (still-running) total. Paused state keeps the live numbers visible.
func formatTooltip(s scheduler.Status, paused bool) string {
	totalSeg := "Total working time: " + formatHHMMpadded(s.TotalWorkingSec)

	if !s.InWorkingHours && !paused {
		return "Outside working hours - " + totalSeg
	}

	var first string
	switch {
	case s.SnoozeActive:
		first = fmt.Sprintf("%s snoozed: %d/%dsec",
			tierShortLabel(s.SnoozeTier), s.SnoozeElapsed, s.SnoozeTotal)
	case !s.AnyEnabled:
		first = "no breaks enabled"
	default:
		var rem int
		switch s.NextNearestKind {
		case scheduler.TierMicro:
			rem = s.MicroRemaining
		case scheduler.TierFull:
			rem = s.FullRemaining
		case scheduler.TierFullRest:
			rem = s.FullRestRemaining
		}
		first = fmt.Sprintf("%s to %s", formatHHMM(rem), tierShortLabel(s.NextNearestKind))
	}

	base := fmt.Sprintf("%s - Since last full: %s - %s",
		first, formatHHMM(s.FullActive), totalSeg)
	if paused {
		return "Paused - " + base
	}
	return base
}

func formatHHMM(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%d:%02d", h, m)
}

// formatHHMMpadded is like formatHHMM but pads the hour to two digits — used
// for the daily total so the tooltip's last segment lines up visually
// regardless of whether the user has worked < 10 h or ≥ 10 h.
func formatHHMMpadded(seconds int) string {
	if seconds < 0 {
		seconds = 0
	}
	h := seconds / 3600
	m := (seconds % 3600) / 60
	return fmt.Sprintf("%02d:%02d", h, m)
}

func tierShortLabel(t scheduler.Tier) string {
	switch t {
	case scheduler.TierMicro:
		return "short break"
	case scheduler.TierFull:
		return "full break"
	case scheduler.TierFullRest:
		return "long rest"
	}
	return "break"
}

func (a *App) buildMenu() error {
	actions := a.ni.ContextMenu().Actions()

	// Pause (checkable, runtime-only, off at startup).
	pause := walk.NewAction()
	if err := pause.SetText("Pause"); err != nil {
		return err
	}
	pause.SetCheckable(true)
	pause.Triggered().Attach(func() {
		// Track state on the App rather than relying on walk's auto-toggle
		// behaviour for checkable actions, which differs across versions.
		a.paused = !a.paused
		a.Log.Info("tray pause toggled", "paused", a.paused)
		if err := pause.SetChecked(a.paused); err != nil {
			a.Log.Warn("set pause checked", "err", err)
		}
		a.Sched.Pause(a.paused)
		if a.paused {
			_ = a.ni.SetIcon(a.pausedIcon)
		} else {
			_ = a.ni.SetIcon(a.activeIcon)
		}
		a.refreshTooltip()
	})
	if err := actions.Add(pause); err != nil {
		return err
	}
	a.pauseAct = pause

	if err := actions.Add(walk.NewSeparatorAction()); err != nil {
		return err
	}

	reset := walk.NewAction()
	_ = reset.SetText("Reset")
	reset.Triggered().Attach(func() {
		a.Log.Info("tray reset clicked")
		a.Sched.Reset()
	})
	if err := actions.Add(reset); err != nil {
		return err
	}

	test := walk.NewAction()
	_ = test.SetText("Test")
	test.Triggered().Attach(func() {
		a.Log.Info("tray test clicked")
		a.Sched.Test()
	})
	if err := actions.Add(test); err != nil {
		return err
	}

	settings := walk.NewAction()
	_ = settings.SetText("Settings…")
	settings.Triggered().Attach(func() {
		a.Log.Info("tray settings opened")
		a.openSettings()
	})
	if err := actions.Add(settings); err != nil {
		return err
	}

	if err := actions.Add(walk.NewSeparatorAction()); err != nil {
		return err
	}

	exit := walk.NewAction()
	_ = exit.SetText("Exit")
	exit.Triggered().Attach(func() {
		a.Log.Info("tray exit clicked")
		a.exiting.Store(true)
		a.closePopup()
		// Dispose the tray icon while the message loop is still alive so
		// Shell_NotifyIcon(NIM_DELETE) succeeds and Windows actually
		// removes the icon (rather than leaving a zombie).
		if a.ni != nil {
			_ = a.ni.Dispose()
			a.ni = nil
		}
		a.Log.Info("limber stopped")
		// walk's PostQuitMessage path through mw.Close() doesn't reliably
		// terminate the process when the parent window is hidden as a
		// 1×1 toolwindow — `go run` was hanging and limber.exe stayed in
		// Task Manager. Force the process to exit; the OS flushes file
		// buffers and releases handles, including the single-instance
		// mutex.
		os.Exit(0)
	})
	if err := actions.Add(exit); err != nil {
		return err
	}

	return nil
}
