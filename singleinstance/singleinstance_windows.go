//go:build windows

// Package singleinstance ensures only one copy of Limber runs at a time on
// Windows by holding a named mutex for the lifetime of the process.
package singleinstance

import (
	"golang.org/x/sys/windows"
)

const mutexName = "LimberSingleInstance_v1"

// Acquire creates the singleton mutex. Returns true if this is the first
// instance and should proceed; false if another instance is already running.
// Callers must keep the returned handle alive for the lifetime of the app —
// the OS releases the mutex when the handle is closed or the process exits.
func Acquire() (windows.Handle, bool) {
	name, err := windows.UTF16PtrFromString(mutexName)
	if err != nil {
		// Fail open: if we can't even encode the name, don't block startup.
		return 0, true
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		// h is still valid — close it; another process holds the lock.
		_ = windows.CloseHandle(h)
		return 0, false
	}
	if err != nil {
		// Unexpected error — fail open rather than refusing to start.
		return h, true
	}
	return h, true
}

// ShowAlreadyRunningDialog blocks on a Win32 MessageBox until the user clicks
// OK, so the user knows why the second launch did nothing.
func ShowAlreadyRunningDialog() {
	const (
		mbOK              = 0x00000000
		mbIconInformation = 0x00000040
		mbTopmost         = 0x00040000
		mbSetForeground   = 0x00010000
	)
	title, _ := windows.UTF16PtrFromString("Limber")
	body, _ := windows.UTF16PtrFromString(
		"Limber is already running.\n\nLook for the stretching-figure icon in the system tray (you may need to click the up-arrow to show hidden icons).")
	_, _ = windows.MessageBox(0, body, title, mbOK|mbIconInformation|mbTopmost|mbSetForeground)
}
