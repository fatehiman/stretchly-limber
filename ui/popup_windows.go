//go:build windows

package ui

import (
	"fmt"
	"image/jpeg"
	"os"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/lxn/walk"
	"github.com/lxn/win"

	"limber/audio"
	"limber/scheduler"
)


type popupWindow struct {
	a   *App
	evt scheduler.Event

	mw *walk.MainWindow
	cw *walk.CustomWidget

	bitmap *walk.Bitmap

	bgBrush     *walk.SolidColorBrush
	closeBrush  *walk.SolidColorBrush
	snoozeBrush *walk.SolidColorBrush
	titleFont   *walk.Font
	textFont    *walk.Font
	countFont   *walk.Font
	stripFont   *walk.Font

	// runtime state
	remaining   time.Duration
	closeOnLeft bool
	disposed    atomic.Bool
	tickStop    chan struct{}

	// mouse / dwell state — only touched on UI thread
	currentStrip int   // 0 none, 1 left, 2 right
	enterID      int64 // monotonic; goroutine validates against this
	// sawMouseMove is false until the first WM_MOUSEMOVE arrives. The very
	// first event only seeds currentStrip from where the cursor already is.
	// Without this guard a popup that opens under a cursor sitting in a
	// strip (with the default 0 ms dwell) dismisses itself instantly.
	sawMouseMove bool
}

func (a *App) openPopup(e scheduler.Event) {
	a.Log.Info("popup show",
		"tier", e.Tier.String(),
		"title", e.Title,
		"image", e.ImagePath,
		"instructions", e.Instructions,
		"durationSec", int(e.Duration.Seconds()),
		"snoozeCount", e.SnoozeCount,
		"idle", e.Idle)
	if a.popup != nil {
		// Defensive: reset before opening a new one.
		a.Log.Debug("openPopup found existing popup, closing first")
		a.closePopup()
	}
	p, err := newPopup(a, e)
	if err != nil {
		a.Log.Error("open popup", "err", err)
		return
	}
	a.popup = p
	if a.Cfg.Audio.Enabled {
		audio.PlayChime()
	}
}

func (a *App) closePopup() {
	if a.popup == nil {
		return
	}
	a.Log.Info("popup close (scheduler-initiated)", "tier", a.popup.evt.Tier.String())
	a.popup.dispose(false)
	a.popup = nil
}

func newPopup(a *App, e scheduler.Event) (*popupWindow, error) {
	p := &popupWindow{
		a:         a,
		evt:       e,
		remaining: e.Duration,
		tickStop:  make(chan struct{}),
	}

	cfg := a.Cfg
	corner := cfg.Popup.Corner
	p.closeOnLeft = corner == "top-left" || corner == "bottom-left"

	mw, err := walk.NewMainWindow()
	if err != nil {
		return nil, err
	}
	p.mw = mw
	mw.SetTitle("Limber Reminder")

	// Strip standard frame and set extended styles BEFORE adding any widgets
	// so walk's layout pass doesn't fight us. Use uint32 throughout to avoid
	// the 0x80000000 (WS_POPUP) constant overflowing int32 at compile time.
	hwnd := mw.Handle()
	style := uint32(win.GetWindowLong(hwnd, win.GWL_STYLE))
	style &^= win.WS_CAPTION | win.WS_THICKFRAME | win.WS_MINIMIZEBOX | win.WS_MAXIMIZEBOX | win.WS_SYSMENU
	style |= win.WS_POPUP
	win.SetWindowLong(hwnd, win.GWL_STYLE, int32(style))

	ex := uint32(win.GetWindowLong(hwnd, win.GWL_EXSTYLE))
	ex |= win.WS_EX_NOACTIVATE | win.WS_EX_TOPMOST | win.WS_EX_TOOLWINDOW
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, int32(ex))

	// Load resources.
	if err := p.makeResources(); err != nil {
		mw.Dispose()
		return nil, err
	}

	// walk.MainWindow REQUIRES a layout — without one, WM_WINDOWPOSCHANGED
	// panics on nil layout in startLayout. Use a zero-margin VBox so the
	// CustomWidget fills the client area.
	layout := walk.NewVBoxLayout()
	_ = layout.SetMargins(walk.Margins{0, 0, 0, 0})
	_ = layout.SetSpacing(0)
	if err := mw.SetLayout(layout); err != nil {
		p.releaseResources()
		mw.Dispose()
		return nil, err
	}

	// CustomWidget filling the window. Pin its min/max size to the configured
	// popup pixels — otherwise the layout would shrink it to size-hint zero,
	// which caused the "only C visible" bug in the previous attempt.
	w, h := cfg.Popup.Width, cfg.Popup.Height
	cw, err := walk.NewCustomWidget(mw, 0, p.paint)
	if err != nil {
		p.releaseResources()
		mw.Dispose()
		return nil, err
	}
	p.cw = cw
	_ = cw.SetMinMaxSizePixels(walk.Size{Width: w, Height: h}, walk.Size{Width: w, Height: h})

	if e.ImagePath != "" {
		if bm, berr := loadJPEGBitmap(e.ImagePath); berr == nil {
			p.bitmap = bm
		} else {
			a.Log.Warn("load image", "path", e.ImagePath, "err", berr)
		}
	}

	// Final position + size in one call.
	x, y := computePopupPosition(corner, w, h, cfg.Popup.HorizontalPaddingPx, cfg.Popup.VerticalPaddingPx)
	win.SetWindowPos(hwnd, win.HWND_TOPMOST, int32(x), int32(y), int32(w), int32(h),
		win.SWP_FRAMECHANGED|win.SWP_NOACTIVATE)

	cw.MouseMove().Attach(p.onMouseMove)

	// Show without activating, then start the countdown ticker.
	win.ShowWindow(hwnd, win.SW_SHOWNOACTIVATE)
	cw.Invalidate()
	a.Log.Info("popup shown", "x", x, "y", y, "w", w, "h", h, "corner", corner, "tier", e.Tier.String())

	go p.tickLoop()
	return p, nil
}

func (p *popupWindow) makeResources() error {
	bg, err := walk.NewSolidColorBrush(walk.RGB(0x1e, 0x1e, 0x1e))
	if err != nil {
		return err
	}
	p.bgBrush = bg

	closeBr, err := walk.NewSolidColorBrush(walk.RGB(0xf4, 0xa8, 0xa8))
	if err != nil {
		return err
	}
	p.closeBrush = closeBr

	snoozeBr, err := walk.NewSolidColorBrush(walk.RGB(0xf5, 0xe6, 0xa3))
	if err != nil {
		return err
	}
	p.snoozeBrush = snoozeBr

	tf, err := walk.NewFont("Segoe UI", 11, walk.FontBold)
	if err != nil {
		return err
	}
	p.titleFont = tf

	bf, err := walk.NewFont("Segoe UI", 9, 0)
	if err != nil {
		return err
	}
	p.textFont = bf

	cf, err := walk.NewFont("Segoe UI", 18, walk.FontBold)
	if err != nil {
		return err
	}
	p.countFont = cf

	sf, err := walk.NewFont("Segoe UI", 7, walk.FontBold)
	if err != nil {
		return err
	}
	p.stripFont = sf
	return nil
}

func (p *popupWindow) releaseResources() {
	if p.bgBrush != nil {
		p.bgBrush.Dispose()
	}
	if p.closeBrush != nil {
		p.closeBrush.Dispose()
	}
	if p.snoozeBrush != nil {
		p.snoozeBrush.Dispose()
	}
	if p.titleFont != nil {
		p.titleFont.Dispose()
	}
	if p.textFont != nil {
		p.textFont.Dispose()
	}
	if p.countFont != nil {
		p.countFont.Dispose()
	}
	if p.stripFont != nil {
		p.stripFont.Dispose()
	}
	if p.bitmap != nil {
		p.bitmap.Dispose()
	}
}

// dispose tears down the popup. submitOnUserAction is true when a hover-trigger
// already submitted a Result; false on scheduler-originated EvtClose.
func (p *popupWindow) dispose(_ bool) {
	if !p.disposed.CompareAndSwap(false, true) {
		return
	}
	close(p.tickStop)
	if p.mw != nil {
		p.mw.Dispose()
	}
	p.releaseResources()
}

// tickLoop drives the countdown.
func (p *popupWindow) tickLoop() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-p.tickStop:
			return
		case <-t.C:
			if p.disposed.Load() {
				return
			}
			next := p.remaining - time.Second
			if next < 0 {
				next = 0
			}
			p.remaining = next
			p.invalidate()
			if p.remaining == 0 && !p.evt.Idle {
				// Regular popup: countdown end == completed.
				p.a.Log.Info("popup countdown ended (auto-complete)", "tier", p.evt.Tier.String())
				p.finish(scheduler.ResCompleted)
				return
			}
		}
	}
}

func (p *popupWindow) invalidate() {
	if p.disposed.Load() {
		return
	}
	mw := p.mw
	cw := p.cw
	if mw == nil || cw == nil {
		return
	}
	mw.Synchronize(func() {
		if p.disposed.Load() {
			return
		}
		cw.Invalidate()
	})
}

// finish is called from any goroutine. It submits the result and disposes.
func (p *popupWindow) finish(action scheduler.ResultAction) {
	if p.disposed.Load() {
		return
	}
	tier := p.evt.Tier
	a := p.a
	verb := "completed"
	if action == scheduler.ResSnoozed {
		verb = "snoozed"
	}
	a.Log.Info("popup close (user)", "action", verb, "tier", tier.String())
	p.a.mw.Synchronize(func() {
		if p.disposed.Load() {
			return
		}
		a.Sched.SubmitResult(scheduler.Result{Tier: tier, Action: action})
		p.dispose(true)
		if a.popup == p {
			a.popup = nil
		}
	})
}

func (p *popupWindow) onMouseMove(x, y int, button walk.MouseButton) {
	p.a.Log.Debug("mouse move on popup", "x", x, "y", y)
	w := p.a.Cfg.Popup.Width
	edge := p.a.Cfg.Popup.EdgeTriggerPx
	if edge < 1 {
		edge = 1
	}
	var newStrip int
	if x < edge {
		newStrip = 1
	} else if x >= w-edge {
		newStrip = 2
	} else {
		newStrip = 0
	}
	if !p.sawMouseMove {
		// First event: always seed currentStrip = 0, regardless of where the
		// cursor actually sits. If the popup happened to open under a cursor
		// already inside a strip, seeding to that strip would mean the user
		// must leave and re-enter to trigger — counter-intuitive. With the
		// 0-seed, the very next mouse move (still inside the strip) registers
		// as a fresh entry and fires after the dwell. The "no auto-fire on
		// open" guarantee is preserved because we still don't trigger on this
		// first event.
		p.sawMouseMove = true
		p.currentStrip = 0
		p.a.Log.Debug("popup first mouse-move seeded", "actualStrip", newStrip, "seededAs", 0, "x", x)
		return
	}
	if newStrip == p.currentStrip {
		return
	}
	p.currentStrip = newStrip
	p.enterID++
	if newStrip == 0 {
		p.a.Log.Debug("popup hover left strip")
		return
	}
	side := "left"
	if newStrip == 2 {
		side = "right"
	}
	p.a.Log.Debug("popup hover entered strip", "side", side, "x", x, "edge", edge)
	myID := p.enterID
	dwell := time.Duration(p.a.Cfg.Popup.EdgeDwellMs) * time.Millisecond
	stripID := newStrip
	if dwell <= 0 {
		// Zero-dwell: trigger immediately on the UI thread.
		p.a.mw.Synchronize(func() {
			if p.disposed.Load() {
				return
			}
			if p.enterID != myID || p.currentStrip != stripID {
				return
			}
			p.triggerSide(stripID)
		})
		return
	}
	go func() {
		time.Sleep(dwell)
		if p.disposed.Load() {
			return
		}
		p.a.mw.Synchronize(func() {
			if p.disposed.Load() {
				return
			}
			if p.enterID != myID || p.currentStrip != stripID {
				return
			}
			p.triggerSide(stripID)
		})
	}()
}

func (p *popupWindow) triggerSide(side int) {
	leftIsClose := p.closeOnLeft
	isClose := (side == 1 && leftIsClose) || (side == 2 && !leftIsClose)
	sideName := "left"
	if side == 2 {
		sideName = "right"
	}
	action := "snooze"
	if isClose {
		action = "close"
	}
	p.a.Log.Debug("popup strip triggered", "side", sideName, "action", action)
	if isClose {
		p.finish(scheduler.ResCompleted)
	} else {
		p.finish(scheduler.ResSnoozed)
	}
}

func (p *popupWindow) paint(canvas *walk.Canvas, _ walk.Rectangle) error {
	cb := p.cw.ClientBoundsPixels()
	w, h := cb.Width, cb.Height
	if w <= 0 || h <= 0 {
		return nil
	}
	edge := p.a.Cfg.Popup.EdgeTriggerPx
	if edge < 1 {
		edge = 1
	}
	if edge*2 >= w {
		edge = w / 5
	}

	// Background.
	full := walk.Rectangle{X: 0, Y: 0, Width: w, Height: h}
	if err := canvas.FillRectanglePixels(p.bgBrush, full); err != nil {
		return err
	}

	innerLeft := edge
	innerRight := w - edge
	innerWidth := innerRight - innerLeft
	if innerWidth < 1 {
		innerWidth = 1
	}

	// Bottom band: countdown. Sized as a fraction of height so 100 px popups
	// still get a readable countdown line.
	countH := h / 4
	if countH < 18 {
		countH = 18
	}
	if countH > 36 {
		countH = 36
	}
	countY := h - countH

	// Above countdown: snooze marker (visible only after a snooze).
	snoozeH := 0
	if p.evt.SnoozeCount > 0 {
		snoozeH = 12
	}
	snoozeY := countY - snoozeH

	// Above snooze: instructions / title combined area. For tiny popups
	// (< 130 px tall) we show only the title and skip wrapped instructions.
	textTop := 4
	instrTop := snoozeY
	titleH := 14
	titleY := instrTop - titleH - 2
	if titleY < textTop+1 {
		titleY = textTop + 1
	}

	// Image area: top of popup, above title.
	imgY := textTop
	imgH := titleY - imgY - 2
	if imgH < 0 {
		imgH = 0
	}

	// 1. Image (or fallback title if no image).
	if imgH > 8 {
		imgRect := walk.Rectangle{X: innerLeft + 4, Y: imgY, Width: innerWidth - 8, Height: imgH}
		if p.bitmap != nil {
			_ = canvas.DrawImageStretchedPixels(p.bitmap, imgRect)
		} else {
			_ = canvas.DrawTextPixels(p.evt.Title, p.titleFont, walk.RGB(0xee, 0xee, 0xee),
				imgRect, walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)
		}
	}

	// 2. Title (only if we have room AND we drew an image — otherwise the
	//    fallback above already shows the title in the image area).
	if titleY+titleH < snoozeY && p.bitmap != nil {
		_ = canvas.DrawTextPixels(p.evt.Title, p.titleFont, walk.RGB(0xee, 0xee, 0xee),
			walk.Rectangle{X: innerLeft, Y: titleY, Width: innerWidth, Height: titleH},
			walk.TextCenter|walk.TextSingleLine)
	}

	// 3. Snooze marker.
	if snoozeH > 0 {
		_ = canvas.DrawTextPixels(snoozeMarker(p.evt.SnoozeCount),
			p.textFont, walk.RGB(0xff, 0xc6, 0x6e),
			walk.Rectangle{X: innerLeft, Y: snoozeY, Width: innerWidth, Height: snoozeH},
			walk.TextCenter|walk.TextSingleLine)
	}

	// 4. Countdown.
	countTxt := formatDuration(p.remaining)
	if p.evt.Idle {
		countTxt = countTxt + "  •  return to close"
	}
	_ = canvas.DrawTextPixels(countTxt, p.countFont, walk.RGB(0xff, 0xff, 0xff),
		walk.Rectangle{X: innerLeft, Y: countY, Width: innerWidth, Height: countH},
		walk.TextCenter|walk.TextVCenter|walk.TextSingleLine)

	// 5. Edge strips.
	leftRect := walk.Rectangle{X: 0, Y: 0, Width: edge, Height: h}
	rightRect := walk.Rectangle{X: w - edge, Y: 0, Width: edge, Height: h}
	if p.closeOnLeft {
		_ = canvas.FillRectanglePixels(p.closeBrush, leftRect)
		_ = canvas.FillRectanglePixels(p.snoozeBrush, rightRect)
		drawVerticalLetters(canvas, p.stripFont, walk.RGB(0xff, 0xff, 0xff), leftRect, "CLOSE")
		drawVerticalLetters(canvas, p.stripFont, walk.RGB(0x33, 0x33, 0x33), rightRect, "SNOOZE")
	} else {
		_ = canvas.FillRectanglePixels(p.snoozeBrush, leftRect)
		_ = canvas.FillRectanglePixels(p.closeBrush, rightRect)
		drawVerticalLetters(canvas, p.stripFont, walk.RGB(0x33, 0x33, 0x33), leftRect, "SNOOZE")
		drawVerticalLetters(canvas, p.stripFont, walk.RGB(0xff, 0xff, 0xff), rightRect, "CLOSE")
	}
	return nil
}

func drawVerticalLetters(canvas *walk.Canvas, font *walk.Font, color walk.Color, area walk.Rectangle, word string) {
	letters := []rune(word)
	if len(letters) == 0 {
		return
	}
	step := area.Height / (len(letters) + 1)
	for i, r := range letters {
		y := area.Y + step*(i+1) - 6
		_ = canvas.DrawTextPixels(string(r), font, color,
			walk.Rectangle{X: area.X, Y: y, Width: area.Width, Height: 12},
			walk.TextCenter|walk.TextSingleLine)
	}
}

func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d", m, s)
}

func snoozeMarker(n int) string {
	suffix := "th"
	switch {
	case n%10 == 1 && n%100 != 11:
		suffix = "st"
	case n%10 == 2 && n%100 != 12:
		suffix = "nd"
	case n%10 == 3 && n%100 != 13:
		suffix = "rd"
	}
	return fmt.Sprintf("%d%s snooze", n, suffix)
}

// SPI_GETWORKAREA is not exported by lxn/win; declare the Win32 constant
// locally. Value from user32.h.
const spiGetWorkArea = 0x0030

func computePopupPosition(corner string, w, h, padX, padY int) (int, int) {
	var rect win.RECT
	if !win.SystemParametersInfo(spiGetWorkArea, 0, unsafe.Pointer(&rect), 0) {
		// Fallback to primary screen metrics (no taskbar awareness).
		sw := int(win.GetSystemMetrics(win.SM_CXSCREEN))
		sh := int(win.GetSystemMetrics(win.SM_CYSCREEN))
		switch corner {
		case "top-left":
			return padX, padY
		case "top-right":
			return sw - w - padX, padY
		case "bottom-left":
			return padX, sh - h - padY
		default: // bottom-right
			return sw - w - padX, sh - h - padY
		}
	}
	switch corner {
	case "top-left":
		return int(rect.Left) + padX, int(rect.Top) + padY
	case "top-right":
		return int(rect.Right) - w - padX, int(rect.Top) + padY
	case "bottom-left":
		return int(rect.Left) + padX, int(rect.Bottom) - h - padY
	default: // bottom-right
		return int(rect.Right) - w - padX, int(rect.Bottom) - h - padY
	}
}

func loadJPEGBitmap(path string) (*walk.Bitmap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return nil, err
	}
	return walk.NewBitmapFromImageForDPI(img, 96)
}
