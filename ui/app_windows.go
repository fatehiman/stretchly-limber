//go:build windows

// Package ui implements the Windows tray, popup, and settings UI.
package ui

import (
	"log/slog"
	"sync/atomic"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"limber/autostart"
	"limber/config"
	"limber/scheduler"
)

// App owns the long-lived UI state: hidden main window, tray icon, current
// popup (if any), and references to the scheduler and config.
type App struct {
	Cfg       *config.Config
	Sched     *scheduler.Scheduler
	Autostart autostart.Manager
	Log       *slog.Logger

	mw       *walk.MainWindow
	ni       *walk.NotifyIcon
	pauseAct *walk.Action
	paused   bool
	exiting  atomic.Bool

	activeIcon *walk.Icon
	pausedIcon *walk.Icon

	popup *popupWindow
}

// Run installs the tray, listens for scheduler events, and blocks on the
// Windows message loop until the user picks Exit.
func (a *App) Run() error {
	if a.Log == nil {
		a.Log = slog.Default()
	}

	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	a.mw = mw
	mw.SetTitle("Limber")
	mw.SetVisible(false)
	// walk.MainWindow panics in WM_WINDOWPOSCHANGED if its layout is nil,
	// even when the window is hidden. A modal dialog closing triggers
	// SetWindowPos on its owner, which is *this* hidden window. Set an
	// empty VBox so the layout pass has something to operate on.
	if err := mw.SetLayout(walk.NewVBoxLayout()); err != nil {
		return err
	}
	// Make the parent window invisible to the user under all circumstances.
	// walk's modal-dialog teardown briefly re-shows the owner; we tuck it at
	// 1x1 offscreen with WS_EX_TOOLWINDOW (no taskbar) and WS_EX_NOACTIVATE
	// (never steals focus) so any flash is unobservable.
	hwnd := mw.Handle()
	exStyle := uint32(win.GetWindowLong(hwnd, win.GWL_EXSTYLE))
	exStyle |= win.WS_EX_TOOLWINDOW | win.WS_EX_NOACTIVATE
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, int32(exStyle))
	win.SetWindowPos(hwnd, 0, -32000, -32000, 1, 1, win.SWP_NOACTIVATE|win.SWP_NOZORDER)

	// If walk's modal-dialog cleanup ever shows our hidden window, intercept
	// its WM_CLOSE so clicking X never quits the message loop. Just hide.
	// The exiting flag is set by the tray Exit menu — when true we let the
	// close go through so the message loop actually exits.
	mw.Closing().Attach(func(canceled *bool, reason walk.CloseReason) {
		if a.exiting.Load() {
			return
		}
		a.Log.Debug("mw close intercepted, hiding instead", "reason", reason)
		*canceled = true
		a.mw.SetVisible(false)
	})

	if err := a.installTray(); err != nil {
		return err
	}
	defer func() {
		// The Exit handler disposes the NotifyIcon synchronously and nils
		// a.ni; any other code path (e.g. an unexpected mw close) still
		// needs the icon removed.
		if a.ni != nil {
			_ = a.ni.Dispose()
			a.ni = nil
		}
		if a.activeIcon != nil {
			a.activeIcon.Dispose()
		}
		if a.pausedIcon != nil {
			a.pausedIcon.Dispose()
		}
	}()

	go a.consumeEvents()

	mw.Run()
	return nil
}

// consumeEvents reads scheduler events and marshals them onto the UI thread.
func (a *App) consumeEvents() {
	for evt := range a.Sched.Events() {
		e := evt
		a.mw.Synchronize(func() {
			a.handleEvent(e)
		})
	}
}

func (a *App) handleEvent(e scheduler.Event) {
	switch e.Type {
	case scheduler.EvtShow:
		a.openPopup(e)
	case scheduler.EvtClose:
		a.closePopup()
	}
}
