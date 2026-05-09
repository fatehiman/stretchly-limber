// Package logger wraps slog with a runtime-mutable level so the user can
// change verbosity from the settings dialog without restarting Limber.
//
// Levels:
//   - "off"   — silence everything
//   - "error" — errors only
//   - "info"  — events + errors (default)
//   - "debug" — everything (very chatty)
package logger

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"limber/config"
)

// silenceLevel is higher than slog.LevelError, so any record with a real
// level is below the threshold and dropped. Used for "off" mode.
const silenceLevel = slog.Level(99)

// LevelVar is the live level the active handler reads from. Changing it via
// SetLevel takes effect on the very next log call.
var LevelVar = &slog.LevelVar{}

// New opens (or creates) `limber.log` in the data directory and returns a
// configured *slog.Logger. The returned closer should be deferred to release
// the file when the program exits.
func New(initial string) (*slog.Logger, io.Closer, error) {
	LevelVar.Set(parse(initial))
	logPath := filepath.Join(config.DataDir(), "limber.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Fallback to stderr.
		h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: LevelVar})
		return slog.New(h), nopCloser{}, err
	}
	h := slog.NewTextHandler(f, &slog.HandlerOptions{Level: LevelVar})
	return slog.New(h), f, nil
}

// SetLevel updates the live level. Accepts "off", "error", "info", "debug".
// Anything unrecognised is treated as "info".
func SetLevel(name string) {
	LevelVar.Set(parse(name))
}

// CurrentLevelName returns the current level as a settings-friendly string.
func CurrentLevelName() string {
	switch LevelVar.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelInfo:
		return "info"
	case slog.LevelError:
		return "error"
	case silenceLevel:
		return "off"
	default:
		return "info"
	}
}

func parse(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	case "off":
		return silenceLevel
	default:
		return slog.LevelInfo
	}
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
