//go:build linux

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
)

type linuxManager struct{}

func New() Manager { return linuxManager{} }

func desktopFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "autostart", "limber.desktop"), nil
}

func (linuxManager) IsEnabled() (bool, error) {
	p, err := desktopFilePath()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (linuxManager) Apply(enabled bool) error {
	p, err := desktopFilePath()
	if err != nil {
		return err
	}
	if !enabled {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s
Exec=%s
X-GNOME-Autostart-enabled=true
NoDisplay=false
Terminal=false
`, AppName, exe)
	return os.WriteFile(p, []byte(body), 0o644)
}
