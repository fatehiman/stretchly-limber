//go:build windows

package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	d "github.com/lxn/walk/declarative"
	"github.com/lxn/win"

	"limber/config"
	"limber/logger"
)

// openSettings shows the modal settings dialog. Save persists to config.json
// and applies live; Cancel discards edits.
func (a *App) openSettings() {
	images := scanExerciseImages()

	// Working copy bound to controls.
	work := *a.Cfg
	// Note: maps/slices in Tiers.Full.Rotation share backing — not edited in v1 UI.

	var dlg *walk.Dialog

	// Field bindings.
	var (
		workStartLE, workEndLE                                                  *walk.LineEdit
		idleResetNE, idleProbeNE                                                *walk.NumberEdit
		startAtBootCB                                                           *walk.CheckBox
		logLevelCB                                                              *walk.ComboBox
		cornerCB                                                                *walk.ComboBox
		hpadNE, vpadNE, edgeTrigNE, edgeDwellNE, snoozeMinNE *walk.NumberEdit
		audioEnabledCB                                                          *walk.CheckBox
		microEnabledCB, fullEnabledCB, fullRestEnabledCB                        *walk.CheckBox
		microIntNE, microDurNE                                                  *walk.NumberEdit
		microImgCB                                                              *walk.ComboBox
		microInstrLE                                                            *walk.LineEdit
		fullIntNE, fullDurNE                                                    *walk.NumberEdit
		fullRestIntNE, fullRestDurNE                                            *walk.NumberEdit
		fullRestImgCB                                                           *walk.ComboBox
		fullRestInstrLE                                                         *walk.LineEdit
	)

	corners := []string{"top-left", "top-right", "bottom-left", "bottom-right"}
	cornerIdx := indexOf(corners, work.Popup.Corner)
	if cornerIdx < 0 {
		cornerIdx = 2
	}

	logLevels := []string{"info", "debug", "error", "off"}
	logLevelIdx := indexOf(logLevels, work.LogLevel)
	if logLevelIdx < 0 {
		logLevelIdx = 0
	}

	microImgIdx := indexOf(images, work.Tiers.Micro.Image)
	fullRestImgIdx := indexOf(images, work.Tiers.FullRest.Image)

	defaultsBtn := d.PushButton{
		Text: "Default",
		OnClicked: func() {
			def := config.Default()
			workStartLE.SetText(def.WorkingHours.Start)
			workEndLE.SetText(def.WorkingHours.End)
			idleResetNE.SetValue(float64(def.IdleResetSeconds))
			idleProbeNE.SetValue(float64(def.IdleProbeSeconds))
			startAtBootCB.SetChecked(def.StartAtBoot)
			cornerCB.SetCurrentIndex(indexOf(corners, def.Popup.Corner))
			hpadNE.SetValue(float64(def.Popup.HorizontalPaddingPx))
			vpadNE.SetValue(float64(def.Popup.VerticalPaddingPx))
			edgeTrigNE.SetValue(float64(def.Popup.EdgeTriggerPx))
			edgeDwellNE.SetValue(float64(def.Popup.EdgeDwellMs))
			snoozeMinNE.SetValue(float64(def.Popup.SnoozeMinutes))
			audioEnabledCB.SetChecked(def.Audio.Enabled)
			if i := indexOf(logLevels, def.LogLevel); i >= 0 {
				logLevelCB.SetCurrentIndex(i)
			}
			microEnabledCB.SetChecked(def.Tiers.Micro.Enabled)
			fullEnabledCB.SetChecked(def.Tiers.Full.Enabled)
			fullRestEnabledCB.SetChecked(def.Tiers.FullRest.Enabled)
			microIntNE.SetValue(float64(def.Tiers.Micro.IntervalMin))
			microDurNE.SetValue(float64(def.Tiers.Micro.DurationSec))
			if i := indexOf(images, def.Tiers.Micro.Image); i >= 0 {
				microImgCB.SetCurrentIndex(i)
			}
			microInstrLE.SetText(def.Tiers.Micro.Instructions)
			fullIntNE.SetValue(float64(def.Tiers.Full.IntervalMin))
			fullDurNE.SetValue(float64(def.Tiers.Full.DurationSec))
			fullRestIntNE.SetValue(float64(def.Tiers.FullRest.IntervalMin))
			fullRestDurNE.SetValue(float64(def.Tiers.FullRest.DurationSec))
			if i := indexOf(images, def.Tiers.FullRest.Image); i >= 0 {
				fullRestImgCB.SetCurrentIndex(i)
			}
			fullRestInstrLE.SetText(def.Tiers.FullRest.Instructions)
		},
	}

	saveBtn := d.PushButton{
		Text: "Save",
		OnClicked: func() {
			newCfg := work
			newCfg.WorkingHours.Start = strings.TrimSpace(workStartLE.Text())
			newCfg.WorkingHours.End = strings.TrimSpace(workEndLE.Text())
			newCfg.IdleResetSeconds = int(idleResetNE.Value())
			newCfg.IdleProbeSeconds = int(idleProbeNE.Value())
			newCfg.StartAtBoot = startAtBootCB.Checked()
			if i := cornerCB.CurrentIndex(); i >= 0 && i < len(corners) {
				newCfg.Popup.Corner = corners[i]
			}
			newCfg.Popup.HorizontalPaddingPx = int(hpadNE.Value())
			newCfg.Popup.VerticalPaddingPx = int(vpadNE.Value())
			newCfg.Popup.EdgeTriggerPx = int(edgeTrigNE.Value())
			newCfg.Popup.EdgeDwellMs = int(edgeDwellNE.Value())
			newCfg.Popup.SnoozeMinutes = int(snoozeMinNE.Value())
			newCfg.Audio.Enabled = audioEnabledCB.Checked()
			if i := logLevelCB.CurrentIndex(); i >= 0 && i < len(logLevels) {
				newCfg.LogLevel = logLevels[i]
			}
			newCfg.Tiers.Micro.Enabled = microEnabledCB.Checked()
			newCfg.Tiers.Micro.IntervalMin = int(microIntNE.Value())
			newCfg.Tiers.Micro.DurationSec = int(microDurNE.Value())
			if i := microImgCB.CurrentIndex(); i >= 0 && i < len(images) {
				newCfg.Tiers.Micro.Image = images[i]
			}
			newCfg.Tiers.Micro.Instructions = microInstrLE.Text()
			newCfg.Tiers.Full.Enabled = fullEnabledCB.Checked()
			newCfg.Tiers.Full.IntervalMin = int(fullIntNE.Value())
			newCfg.Tiers.Full.DurationSec = int(fullDurNE.Value())
			newCfg.Tiers.FullRest.Enabled = fullRestEnabledCB.Checked()
			newCfg.Tiers.FullRest.IntervalMin = int(fullRestIntNE.Value())
			newCfg.Tiers.FullRest.DurationSec = int(fullRestDurNE.Value())
			if i := fullRestImgCB.CurrentIndex(); i >= 0 && i < len(images) {
				newCfg.Tiers.FullRest.Image = images[i]
			}
			newCfg.Tiers.FullRest.Instructions = fullRestInstrLE.Text()

			if err := validate(&newCfg); err != nil {
				walk.MsgBox(dlg, "Invalid settings", err.Error(),
					walk.MsgBoxIconWarning)
				return
			}

			oldStartAtBoot := a.Cfg.StartAtBoot
			oldLogLevel := a.Cfg.LogLevel
			a.Cfg.Replace(&newCfg)
			if err := a.Cfg.Save(); err != nil {
				walk.MsgBox(dlg, "Save failed", err.Error(), walk.MsgBoxIconError)
				return
			}
			if newCfg.LogLevel != oldLogLevel {
				logger.SetLevel(newCfg.LogLevel)
				a.Log.Info("log level changed", "from", oldLogLevel, "to", newCfg.LogLevel)
			}
			if newCfg.StartAtBoot != oldStartAtBoot {
				if err := a.Autostart.Apply(newCfg.StartAtBoot); err != nil {
					walk.MsgBox(dlg, "Autostart toggle failed", err.Error(), walk.MsgBoxIconWarning)
					a.Log.Error("autostart apply", "err", err)
				} else {
					a.Log.Info("autostart applied", "enabled", newCfg.StartAtBoot)
				}
			}
			a.Log.Info("settings saved")
			dlg.Accept()
		},
	}

	cancelBtn := d.PushButton{
		Text: "Cancel",
		OnClicked: func() {
			dlg.Cancel()
		},
	}

	imageItems := images
	if len(imageItems) == 0 {
		imageItems = []string{"(no images in assets/exercises)"}
	}

	form := d.Dialog{
		AssignTo: &dlg,
		Title:    "Limber Settings",
		MinSize:  d.Size{Width: 520, Height: 560},
		Layout:   d.VBox{},
		Children: []d.Widget{
			// Enabled toggles for each break tier, sitting above the tabs so
			// they're visible regardless of which tab is open. Per-tier
			// settings stay editable even when Enabled is off — the checkbox
			// is purely a runtime gate (counter freezes, no popups), not a
			// UI gate.
			d.GroupBox{
				Title:  "Enabled breaks",
				Layout: d.HBox{},
				Children: []d.Widget{
					d.CheckBox{AssignTo: &microEnabledCB, Text: "Micro break", Checked: work.Tiers.Micro.Enabled},
					d.CheckBox{AssignTo: &fullEnabledCB, Text: "Full break", Checked: work.Tiers.Full.Enabled},
					d.CheckBox{AssignTo: &fullRestEnabledCB, Text: "Full rest", Checked: work.Tiers.FullRest.Enabled},
					d.HSpacer{},
				},
			},
			d.TabWidget{
				Pages: []d.TabPage{
					{
						Title:  "General",
						Layout: d.Grid{Columns: 2},
						Children: []d.Widget{
							d.Label{Text: "Working hours start (HH:MM):"},
							d.LineEdit{AssignTo: &workStartLE, Text: work.WorkingHours.Start},
							d.Label{Text: "Working hours end (HH:MM):"},
							d.LineEdit{AssignTo: &workEndLE, Text: work.WorkingHours.End},
							d.Label{Text: "Idle reset (seconds):"},
							d.NumberEdit{AssignTo: &idleResetNE, MinValue: 30, MaxValue: 7200, Value: float64(work.IdleResetSeconds)},
							d.Label{Text: "Idle probe (seconds):"},
							d.NumberEdit{AssignTo: &idleProbeNE, MinValue: 5, MaxValue: 600, Value: float64(work.IdleProbeSeconds)},
							d.Label{Text: "Start at boot:"},
							d.CheckBox{AssignTo: &startAtBootCB, Checked: work.StartAtBoot},
							d.Label{Text: "Log level:"},
							d.ComboBox{AssignTo: &logLevelCB, Model: logLevels, CurrentIndex: logLevelIdx},
						},
					},
					{
						Title:  "Popup",
						Layout: d.Grid{Columns: 2},
						Children: []d.Widget{
							d.Label{Text: "Corner:"},
							d.ComboBox{AssignTo: &cornerCB, Model: corners, CurrentIndex: cornerIdx},
							d.Label{Text: "Horizontal padding (px):"},
							d.NumberEdit{AssignTo: &hpadNE, MinValue: 0, MaxValue: 1000, Value: float64(work.Popup.HorizontalPaddingPx)},
							d.Label{Text: "Vertical padding (px):"},
							d.NumberEdit{AssignTo: &vpadNE, MinValue: 0, MaxValue: 1000, Value: float64(work.Popup.VerticalPaddingPx)},
							d.Label{Text: "Edge trigger (px):"},
							d.NumberEdit{AssignTo: &edgeTrigNE, MinValue: 4, MaxValue: 200, Value: float64(work.Popup.EdgeTriggerPx)},
							d.Label{Text: "Edge dwell (ms):"},
							d.NumberEdit{AssignTo: &edgeDwellNE, MinValue: 0, MaxValue: 2000, Value: float64(work.Popup.EdgeDwellMs)},
							d.Label{Text: "Snooze (minutes):"},
							d.NumberEdit{AssignTo: &snoozeMinNE, MinValue: 1, MaxValue: 60, Value: float64(work.Popup.SnoozeMinutes)},
							d.Label{Text: ""},
							d.Label{Text: "Image size is fixed at 200 × 140 px.\nPopup auto-sizes to fit; width follows edge-trigger."},
						},
					},
					{
						Title:  "Micro break",
						Layout: d.Grid{Columns: 2},
						Children: []d.Widget{
							d.Label{Text: "Interval (minutes):"},
							d.NumberEdit{AssignTo: &microIntNE, MinValue: 1, MaxValue: 240, Value: float64(work.Tiers.Micro.IntervalMin)},
							d.Label{Text: "Duration (seconds):"},
							d.NumberEdit{AssignTo: &microDurNE, MinValue: 5, MaxValue: 600, Value: float64(work.Tiers.Micro.DurationSec)},
							d.Label{Text: "Image:"},
							d.ComboBox{AssignTo: &microImgCB, Model: imageItems, CurrentIndex: microImgIdx},
							d.Label{Text: "Instructions:"},
							d.LineEdit{AssignTo: &microInstrLE, Text: work.Tiers.Micro.Instructions},
						},
					},
					{
						Title:  "Full break",
						Layout: d.VBox{},
						Children: []d.Widget{
							d.Composite{
								Layout: d.Grid{Columns: 2},
								Children: []d.Widget{
									d.Label{Text: "Interval (minutes):"},
									d.NumberEdit{AssignTo: &fullIntNE, MinValue: 1, MaxValue: 240, Value: float64(work.Tiers.Full.IntervalMin)},
									d.Label{Text: "Duration (seconds):"},
									d.NumberEdit{AssignTo: &fullDurNE, MinValue: 10, MaxValue: 1800, Value: float64(work.Tiers.Full.DurationSec)},
								},
							},
							d.Label{Text: "Rotation entries (edit config.json directly to add/remove):"},
							d.TextEdit{Text: rotationSummary(work.Tiers.Full.Rotation), ReadOnly: true},
						},
					},
					{
						Title:  "Full rest",
						Layout: d.Grid{Columns: 2},
						Children: []d.Widget{
							d.Label{Text: "Interval (minutes):"},
							d.NumberEdit{AssignTo: &fullRestIntNE, MinValue: 1, MaxValue: 480, Value: float64(work.Tiers.FullRest.IntervalMin)},
							d.Label{Text: "Duration (seconds):"},
							d.NumberEdit{AssignTo: &fullRestDurNE, MinValue: 30, MaxValue: 3600, Value: float64(work.Tiers.FullRest.DurationSec)},
							d.Label{Text: "Image:"},
							d.ComboBox{AssignTo: &fullRestImgCB, Model: imageItems, CurrentIndex: fullRestImgIdx},
							d.Label{Text: "Instructions:"},
							d.LineEdit{AssignTo: &fullRestInstrLE, Text: work.Tiers.FullRest.Instructions},
						},
					},
					{
						Title:  "Audio",
						Layout: d.Grid{Columns: 2},
						Children: []d.Widget{
							d.Label{Text: "Play soft chime on popup:"},
							d.CheckBox{AssignTo: &audioEnabledCB, Checked: work.Audio.Enabled},
						},
					},
				},
			},
			d.Composite{
				Layout: d.HBox{},
				Children: []d.Widget{
					defaultsBtn,
					d.HSpacer{},
					saveBtn,
					cancelBtn,
				},
			},
		},
	}

	// Walk's declarative Dialog.Run centres the dialog on its owner. Our
	// owner is the hidden 1×1 toolwindow tucked off-screen at (-32000,-32000)
	// so without intervention the dialog lands offscreen / at (0,0). We can't
	// switch to a Create+dlg.Run() split — that was reintroducing a WM_QUIT
	// leak that exited the whole app on close. Instead, temporarily move the
	// owner to the centre of the primary work area for the duration of Run;
	// walk centres the dialog on owner.X/Y regardless of dialog or owner
	// size, so this lands the dialog at screen centre. Restore the owner's
	// off-screen position afterwards.
	mwHwnd := a.mw.Handle()
	var workArea win.RECT
	movedOwner := false
	if win.SystemParametersInfo(spiGetWorkArea, 0, unsafe.Pointer(&workArea), 0) {
		cx := int32(workArea.Left) + (workArea.Right-workArea.Left)/2
		cy := int32(workArea.Top) + (workArea.Bottom-workArea.Top)/2
		win.SetWindowPos(mwHwnd, 0, cx, cy, 0, 0,
			win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
		movedOwner = true
	}

	result, err := form.Run(a.mw)
	if err != nil {
		a.Log.Error("settings dialog", "err", err)
	}
	a.Log.Debug("settings dialog closed", "result", result)

	if movedOwner {
		win.SetWindowPos(mwHwnd, 0, -32000, -32000, 0, 0,
			win.SWP_NOSIZE|win.SWP_NOZORDER|win.SWP_NOACTIVATE)
	}
	// walk's modal-dialog teardown can leave the owner shown — re-hide it
	// explicitly so the empty Limber window doesn't linger after the dialog.
	a.mw.SetVisible(false)
}

func validate(c *config.Config) error {
	if _, err := config.ParseTime(timeRef(), c.WorkingHours.Start); err != nil {
		return fmt.Errorf("working hours start: %w", err)
	}
	if _, err := config.ParseTime(timeRef(), c.WorkingHours.End); err != nil {
		return fmt.Errorf("working hours end: %w", err)
	}
	if c.IdleResetSeconds < 30 {
		return fmt.Errorf("idle reset must be at least 30 seconds")
	}
	if c.IdleProbeSeconds < 5 {
		return fmt.Errorf("idle probe must be at least 5 seconds")
	}
	if c.Popup.EdgeTriggerPx < 4 {
		return fmt.Errorf("edge trigger must be at least 4 px")
	}
	if c.Popup.SnoozeMinutes < 1 {
		return fmt.Errorf("snooze must be at least 1 minute")
	}
	if c.Tiers.Micro.IntervalMin < 1 ||
		c.Tiers.Full.IntervalMin < 1 ||
		c.Tiers.FullRest.IntervalMin < 1 {
		return fmt.Errorf("intervals must be at least 1 minute")
	}
	return nil
}

func rotationSummary(items []config.RotationItem) string {
	if len(items) == 0 {
		return "(none)"
	}
	var sb strings.Builder
	for i, it := range items {
		fmt.Fprintf(&sb, "%d. %s\r\n   image: %s\r\n   %s\r\n", i+1, it.ID, it.Image, it.Instructions)
	}
	return sb.String()
}

func indexOf(items []string, v string) int {
	for i, s := range items {
		if s == v {
			return i
		}
	}
	return -1
}

func scanExerciseImages() []string {
	dir := filepath.Join(config.DataDir(), "assets", "exercises")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".jpg") || strings.HasSuffix(lower, ".jpeg") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// timeRef returns a stable reference time (today at midnight, local) used by
// validate to parse HH:MM strings.
func timeRef() time.Time {
	now := time.Now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

