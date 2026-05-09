//go:build windows

// Package audio plays an optional soft chime when a popup appears.
package audio

import (
	"syscall"
	"unsafe"
)

const (
	sndAsync    = 0x0001
	sndFilename = 0x0002_0000
	sndAlias    = 0x0001_0000
	sndNoStop   = 0x0000_0010
)

var (
	winmm        = syscall.NewLazyDLL("winmm.dll")
	procPlaySndW = winmm.NewProc("PlaySoundW")
)

// PlayChime plays a short, soft system chime asynchronously. If the system
// alias is unavailable for any reason, the call is a no-op.
func PlayChime() {
	// "SystemNotification" is the standard "soft" notification alias on Windows.
	alias, _ := syscall.UTF16PtrFromString("SystemNotification")
	procPlaySndW.Call(
		uintptr(unsafe.Pointer(alias)),
		0,
		uintptr(sndAsync|sndAlias|sndNoStop),
	)
}
