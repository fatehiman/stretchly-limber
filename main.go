// Limber — adaptive break reminder for desk workers.
//
// Single-binary Windows tray app. Plans, layout, and behaviour are documented
// in README.md.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"limber/activity"
	"limber/autostart"
	"limber/config"
	"limber/logger"
	"limber/scheduler"
	"limber/singleinstance"
	"limber/ui"
)

func main() {
	// Anchor the data directory to the executable's folder for installed
	// binaries (so logs/config land beside limber.exe regardless of how it
	// was launched — Explorer, autostart, taskbar shortcut). Skip this
	// under `go run`, where os.Executable() points at a temp build dir.
	chdirToExeDirIfInstalled()

	// Single-instance guard. If another Limber is already running, show an
	// "already running" dialog and exit. The handle is held implicitly for
	// the lifetime of this process; the OS releases the mutex on exit.
	if _, first := singleinstance.Acquire(); !first {
		singleinstance.ShowAlreadyRunningDialog()
		os.Exit(0)
	}

	// Bootstrap a logger at info level so any pre-config errors surface;
	// the level is updated to the saved value once config is loaded.
	log, closer, _ := logger.New("info")
	defer closer.Close()

	cfg, err := config.Load()
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	logger.SetLevel(cfg.LogLevel)
	log.Info("limber starting",
		"configPath", cfg.Path(),
		"logLevel", logger.CurrentLevelName())

	act := activity.New()
	sched := scheduler.New(cfg, act, log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	app := &ui.App{
		Cfg:       cfg,
		Sched:     sched,
		Autostart: autostart.New(),
		Log:       log,
	}
	if err := app.Run(); err != nil {
		log.Error("ui run", "err", err)
		os.Exit(1)
	}
	log.Info("limber stopped")
}

// chdirToExeDirIfInstalled chdirs to the directory of the running executable
// unless it lives in a `go-build` temp directory (i.e. we were launched via
// `go run`), in which case the existing working directory is left alone so
// project data (config.json, limber.log, assets/) stays in the project tree.
func chdirToExeDirIfInstalled() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	exeDir := filepath.Dir(exe)
	lower := strings.ToLower(exeDir)
	if strings.Contains(lower, "go-build") {
		return
	}
	_ = os.Chdir(exeDir)
}
