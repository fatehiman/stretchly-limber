package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const FileName = "config.json"

type WorkingHours struct {
	Start string `json:"start"` // "HH:MM"
	End   string `json:"end"`
}

type Popup struct {
	Corner              string `json:"corner"` // top-left | top-right | bottom-left | bottom-right
	Width               int    `json:"width"`
	Height              int    `json:"height"`
	HorizontalPaddingPx int    `json:"horizontalPaddingPx"`
	VerticalPaddingPx   int    `json:"verticalPaddingPx"`
	EdgeTriggerPx       int    `json:"edgeTriggerPx"`
	EdgeDwellMs         int    `json:"edgeDwellMs"`
	SnoozeMinutes       int    `json:"snoozeMinutes"`
}

type Audio struct {
	Enabled bool `json:"enabled"`
}

type RotationItem struct {
	ID           string `json:"id"`
	Image        string `json:"image"`
	Instructions string `json:"instructions"`
}

type MicroTier struct {
	Enabled      bool   `json:"enabled"`
	IntervalMin  int    `json:"intervalMin"`
	DurationSec  int    `json:"durationSec"`
	Image        string `json:"image"`
	Instructions string `json:"instructions"`
}

type FullTier struct {
	Enabled     bool           `json:"enabled"`
	IntervalMin int            `json:"intervalMin"`
	DurationSec int            `json:"durationSec"`
	Rotation    []RotationItem `json:"rotation"`
}

type FullRestTier struct {
	Enabled      bool   `json:"enabled"`
	IntervalMin  int    `json:"intervalMin"`
	DurationSec  int    `json:"durationSec"`
	Image        string `json:"image"`
	Instructions string `json:"instructions"`
}

type Tiers struct {
	Micro    MicroTier    `json:"micro"`
	Full     FullTier     `json:"full"`
	FullRest FullRestTier `json:"fullRest"`
}

type Config struct {
	WorkingHours     WorkingHours `json:"workingHours"`
	IdleResetSeconds int          `json:"idleResetSeconds"`
	Popup            Popup        `json:"popup"`
	Audio            Audio        `json:"audio"`
	StartAtBoot      bool         `json:"startAtBoot"`
	LogLevel         string       `json:"logLevel"` // info | debug | error | off
	Tiers            Tiers        `json:"tiers"`

	mu   sync.RWMutex
	path string
}

// Default returns the orthopedically-derived defaults.
func Default() *Config {
	return &Config{
		WorkingHours: WorkingHours{
			Start: "09:00",
			End:   "18:00",
		},
		IdleResetSeconds: 300,
		Popup: Popup{
			Corner:              "bottom-left",
			Width:               200,
			Height:              100,
			HorizontalPaddingPx: 0,
			VerticalPaddingPx:   0,
			EdgeTriggerPx:       30,
			EdgeDwellMs:         0,
			SnoozeMinutes:       3,
		},
		Audio: Audio{
			Enabled: false,
		},
		StartAtBoot: false,
		LogLevel:    "info",
		Tiers: Tiers{
			Micro: MicroTier{
				Enabled:      true,
				IntervalMin:  20,
				DurationSec:  20,
				Image:        "eye_2020.jpg",
				Instructions: "Look at something at least 20 feet (6 m) away for 20 seconds.",
			},
			Full: FullTier{
				Enabled:     true,
				IntervalMin: 30,
				DurationSec: 60,
				Rotation: []RotationItem{
					{ID: "chin-tuck", Image: "cervical_retraction.jpg",
						Instructions: "Sit tall. Pull your chin straight back (double-chin). Hold 5 s. Repeat 10x."},
					{ID: "shoulder-rolls", Image: "scapular_retraction.jpg",
						Instructions: "Roll shoulders back 10x. Squeeze shoulder blades together, hold 5 s, repeat 10x."},
					{ID: "wrist-stretch", Image: "wrist_flexor_stretch.jpg",
						Instructions: "Arm out, palm up. Pull fingers down. Hold 20 s each side, both directions."},
					{ID: "lumbar-extension", Image: "lumbar_extension.jpg",
						Instructions: "Stand. Hands on lower back. Gently arch backward. Hold 5 s. Repeat 10x."},
					{ID: "hip-flexor", Image: "hip_flexor_stretch.jpg",
						Instructions: "Step one foot back into a lunge. Tuck pelvis. Hold 30 s each side."},
				},
			},
			FullRest: FullRestTier{
				Enabled:      true,
				IntervalMin:  90,
				DurationSec:  900,
				Image:        "walk_break.jpg",
				Instructions: "Stand up, walk a few minutes, look around the room, do a full-body stretch.",
			},
		},
	}
}

// DataDir returns the directory where config.json, limber.log and
// assets/ live. It's just the current working directory — main.go chdirs to
// the executable's folder on startup for installed binaries, while leaving
// the cwd alone when running under `go run` (where os.Executable() points
// at a Temp\go-build path that's wrong for our data).
func DataDir() string {
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// ConfigPath returns the absolute path of config.json in the data directory.
func ConfigPath() string {
	return filepath.Join(DataDir(), FileName)
}

// Load reads config.json from the data directory, writing defaults if absent.
func Load() (*Config, error) {
	p := ConfigPath()
	cfg := Default()
	cfg.path = p

	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		// Write defaults on first run.
		if err := cfg.Save(); err != nil {
			return nil, fmt.Errorf("write default config: %w", err)
		}
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes the config back to disk atomically.
func (c *Config) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.path == "" {
		c.path = ConfigPath()
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Path returns the path the config is loaded from / saved to.
func (c *Config) Path() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.path
}

// Replace atomically substitutes the public fields with another config's,
// preserving the path and lock.
func (c *Config) Replace(n *Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.WorkingHours = n.WorkingHours
	c.IdleResetSeconds = n.IdleResetSeconds
	c.Popup = n.Popup
	c.Audio = n.Audio
	c.StartAtBoot = n.StartAtBoot
	c.LogLevel = n.LogLevel
	c.Tiers = n.Tiers
}

// ParseTime parses "HH:MM" into a time-of-day on the same day as ref.
func ParseTime(ref time.Time, hhmm string) (time.Time, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, ref.Location()), nil
}

// WithinWorkingHours reports whether now is inside [start, end). If end < start
// the window is treated as crossing midnight.
func (c *Config) WithinWorkingHours(now time.Time) bool {
	c.mu.RLock()
	wh := c.WorkingHours
	c.mu.RUnlock()
	start, err := ParseTime(now, wh.Start)
	if err != nil {
		return true
	}
	end, err := ParseTime(now, wh.End)
	if err != nil {
		return true
	}
	if end.After(start) {
		return !now.Before(start) && now.Before(end)
	}
	// Crosses midnight: working if now >= start OR now < end.
	return !now.Before(start) || now.Before(end)
}
