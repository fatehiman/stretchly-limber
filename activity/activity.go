// Package activity provides a cross-platform interface for reporting
// system-wide user idle time. The active implementation is selected by
// build tags (activity_windows.go, activity_linux.go).
package activity

// Provider reports the number of seconds since the last user input
// across the whole system (mouse or keyboard, in any process).
type Provider interface {
	IdleSeconds() int
}
