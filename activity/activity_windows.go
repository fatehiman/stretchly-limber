//go:build windows

package activity

import (
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type windowsProvider struct{}

// New returns a Provider backed by user32!GetLastInputInfo.
func New() Provider {
	return windowsProvider{}
}

// IdleSeconds returns whole seconds since the last keyboard / mouse input.
func (windowsProvider) IdleSeconds() int {
	var lii lastInputInfo
	lii.cbSize = uint32(unsafe.Sizeof(lii))
	r1, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if r1 == 0 {
		return 0
	}
	tickRet, _, _ := procGetTickCount.Call()
	tick := uint32(tickRet)
	if tick < lii.dwTime {
		return 0
	}
	return int((tick - lii.dwTime) / 1000)
}
