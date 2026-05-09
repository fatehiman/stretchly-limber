// Package autostart manages the OS-level "run at boot" entry for Limber.
package autostart

// Manager applies a desired enabled/disabled state for the current user's
// startup entry pointing at the running executable.
type Manager interface {
	IsEnabled() (bool, error)
	Apply(enabled bool) error
}

// AppName is the per-user identifier used in the OS startup configuration.
const AppName = "Limber"
