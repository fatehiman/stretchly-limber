//go:build linux

package activity

// Linux idle detection is not implemented in v1. The stub always reports 0
// idle seconds, which means idle-as-break and idle resets are inactive.
// A future implementation will call XScreenSaverQueryInfo via libXss.

type linuxStub struct{}

func New() Provider          { return linuxStub{} }
func (linuxStub) IdleSeconds() int { return 0 }
