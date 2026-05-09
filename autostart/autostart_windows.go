//go:build windows

package autostart

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

type windowsManager struct{}

func New() Manager { return windowsManager{} }

func (windowsManager) IsEnabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKey, registry.QUERY_VALUE)
	if err != nil {
		return false, err
	}
	defer k.Close()
	got, _, err := k.GetStringValue(AppName)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		return got != "", nil
	}
	return got != "" && (got == quote(exe) || got == exe), nil
}

func (windowsManager) Apply(enabled bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKey, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Run key: %w", err)
	}
	defer k.Close()
	if !enabled {
		if err := k.DeleteValue(AppName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("delete Run value: %w", err)
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate executable: %w", err)
	}
	return k.SetStringValue(AppName, quote(exe))
}

func quote(s string) string {
	return `"` + s + `"`
}
