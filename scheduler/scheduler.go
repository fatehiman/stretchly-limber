// Package scheduler runs the per-tier active-time counters and decides when
// to fire reminder popups. It is single-goroutine and communicates with the
// UI via channels (commands in, events out, results in).
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"limber/activity"
	"limber/config"
)

// Tier identifies one of the three break categories.
type Tier int

const (
	TierMicro Tier = iota
	TierFull
	TierFullRest
)

func (t Tier) String() string {
	switch t {
	case TierMicro:
		return "micro"
	case TierFull:
		return "full"
	case TierFullRest:
		return "full-rest"
	}
	return "?"
}

// EventType discriminates Show / Close events emitted to the UI.
type EventType int

const (
	EvtShow  EventType = iota // tell the UI to open a popup with the given fields
	EvtClose                  // tell the UI to close any active popup, no result expected
)

// Event is what the scheduler emits on its Events() channel.
type Event struct {
	Type EventType

	// Show-only fields below.
	Tier         Tier
	Idle         bool   // true if this popup is being shown because the user went idle
	IsProbe      bool   // true if this popup is an idle probe (precedes the idle break popup)
	Title        string // display title (e.g. "Eye Break")
	ImagePath    string // absolute path to image file, may be "" if missing
	Instructions string
	Duration     time.Duration // popup countdown duration
	SnoozeCount  int           // 0 = first showing, 1 = first snooze, ...
}

// ResultAction is what the user did with a popup.
type ResultAction int

const (
	ResCompleted          ResultAction = iota // close strip, or countdown reached 0 (regular popup)
	ResSnoozed                                // snooze strip
	ResIdleProbeCancelled                     // probe popup dismissed by user activity — not really idle
	ResIdleProbeExpired                       // probe countdown reached 0 — real idle confirmed
)

// Result is sent from the UI back to the scheduler when a popup closes due
// to user action (not a scheduler-originated EvtClose).
type Result struct {
	Tier   Tier
	Action ResultAction
}

// Scheduler is the long-running tick driver.
type Scheduler struct {
	cfg *config.Config
	act activity.Provider
	log *slog.Logger

	cmds    chan command
	results chan Result
	events  chan Event

	// internal state — only touched by Run.
	microActive     int
	fullActive      int
	fullRestActive  int
	fullRotIdx      int

	firedFor    map[Tier]bool
	snoozeCount map[Tier]int
	pendSnooze  map[Tier]int

	paused       bool
	inIdle       bool
	idleFired    bool // an idle popup (probe or break) has already been shown during the current idle session
	popupActive  bool
	popupTier    Tier
	popupIsIdle  bool
	popupIsProbe bool // current popup is the idle probe, not the break popup
	popupIsTest  bool // current popup was triggered by Test menu — survives the working-hours gate
	queue        []queued

	// daily total of active (non-idle) seconds; resets on local-day rollover.
	totalWorkingSec int
	totalWorkingDay int // YYYYMMDD; 0 means "not initialised yet"

	// captured state for status display
	statusMu sync.Mutex
	status   Status
}

type queued struct {
	tier Tier
	idle bool
}

type command int

const (
	cmdReset command = iota
	cmdTest
	cmdPauseOn
	cmdPauseOff
	cmdShutdown
)

// Status is a snapshot of internal counters for diagnostics / tray tooltip.
type Status struct {
	Paused         bool
	InWorkingHours bool
	IdleSec        int

	// Per-tier enable flags (mirror of cfg.Tiers.*.Enabled, captured per-tick
	// for stable display).
	MicroEnabled    bool
	FullEnabled     bool
	FullRestEnabled bool
	AnyEnabled      bool

	MicroRemaining    int
	FullRemaining     int
	FullRestRemaining int
	FullActive        int  // seconds of active time since last regular Full break completed
	NextNearestKind   Tier // enabled tier whose counter is closest to firing
	PopupActive       bool

	// Snooze state — populated when at least one tier has a pending snooze.
	// SnoozeTier / SnoozeElapsed / SnoozeTotal describe the soonest-to-expire
	// snooze; SnoozeElapsed grows from 0 to SnoozeTotal as the timer drains.
	SnoozeActive  bool
	SnoozeTier    Tier
	SnoozeElapsed int
	SnoozeTotal   int

	// Active (non-idle) seconds since local 00:00 of the current day.
	TotalWorkingSec int
}

// New builds a Scheduler bound to the given config and activity provider.
func New(cfg *config.Config, act activity.Provider, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		cfg:         cfg,
		act:         act,
		log:         log,
		cmds:        make(chan command, 8),
		results:     make(chan Result, 8),
		events:      make(chan Event, 8),
		firedFor:    map[Tier]bool{},
		snoozeCount: map[Tier]int{},
		pendSnooze:  map[Tier]int{},
	}
}

// Events is the channel the UI consumes.
func (s *Scheduler) Events() <-chan Event { return s.events }

// SubmitResult is called by the UI when the user closes / snoozes a popup.
func (s *Scheduler) SubmitResult(r Result) { s.results <- r }

func (s *Scheduler) Reset()      { s.cmds <- cmdReset }
func (s *Scheduler) Test()       { s.cmds <- cmdTest }
func (s *Scheduler) Pause(on bool) {
	if on {
		s.cmds <- cmdPauseOn
	} else {
		s.cmds <- cmdPauseOff
	}
}
func (s *Scheduler) Shutdown() { s.cmds <- cmdShutdown }

// Snapshot returns a copy of the current status for display purposes.
func (s *Scheduler) Snapshot() Status {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	return s.status
}

// Run drives the 1Hz tick loop until ctx is cancelled or shutdown is requested.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.cmds:
			if !s.handleCommand(c) {
				return
			}
		case r := <-s.results:
			s.handleResult(r)
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Scheduler) handleCommand(c command) bool {
	switch c {
	case cmdReset:
		s.log.Info("scheduler reset")
		s.fullReset()
		s.events <- Event{Type: EvtClose}
	case cmdTest:
		// Test: close any active popup first so the next popup uses the
		// current settings (corner, size, image, etc.) and isn't blocked by
		// a stale window.
		if s.popupActive {
			s.log.Debug("test closing existing popup", "tier", s.popupTier.String(), "idle", s.popupIsIdle)
			s.events <- Event{Type: EvtClose}
			s.popupActive = false
			s.popupIsIdle = false
			s.popupIsProbe = false
			s.popupIsTest = false
			s.queue = nil
		}
		tier := s.nextNearestKind()
		s.log.Info("test triggered", "tier", tier.String())
		// Inline-fire (rather than fireOrQueue) so we can mark this popup as
		// a Test popup. The working-hours gate honours that flag and won't
		// auto-close it just because we're outside 9–18.
		s.popupActive = true
		s.popupIsIdle = false
		s.popupIsProbe = false
		s.popupIsTest = true
		s.popupTier = tier
		s.events <- s.makeShowEvent(tier, false)
	case cmdPauseOn:
		s.log.Info("pause on")
		s.paused = true
		if s.popupActive {
			s.events <- Event{Type: EvtClose}
			// Push the suppressed popup back onto the front of the queue so
			// unpause re-fires it. Exceptions: Test popups are user-initiated
			// one-shots; idle break popups and probes belong to a since-passed
			// idle session — none should resurrect on unpause.
			if !s.popupIsTest && !s.popupIsIdle && !s.popupIsProbe {
				s.queue = append([]queued{{tier: s.popupTier, idle: false}}, s.queue...)
			}
			s.popupActive = false
			s.popupIsIdle = false
			s.popupIsProbe = false
			s.popupIsTest = false
		}
	case cmdPauseOff:
		s.log.Info("pause off")
		s.paused = false
		// Re-fire any popup that was on screen (or queued) when pause was
		// engaged. dequeue is a no-op on an empty queue.
		s.dequeue()
	case cmdShutdown:
		s.log.Info("scheduler shutdown")
		s.events <- Event{Type: EvtClose}
		return false
	}
	return true
}

func (s *Scheduler) handleResult(r Result) {
	if !s.popupActive {
		return
	}
	tier := r.Tier
	switch r.Action {
	case ResCompleted:
		s.completeTier(tier)
		s.popupActive = false
		s.popupIsIdle = false
		s.popupIsProbe = false
		s.popupIsTest = false
		s.dequeue()
	case ResSnoozed:
		s.snoozeTier(tier)
		s.popupActive = false
		s.popupIsIdle = false
		s.popupIsProbe = false
		s.popupIsTest = false
		s.dequeue()
	case ResIdleProbeCancelled:
		// User moved during probe — they're at the keyboard after all. Clear
		// popup state but leave idleFired alone: the next tick will see idle
		// drop below threshold and run the idle-exit branch (which resets
		// inIdle / idleFired). Counters resume from their pre-idle values
		// because we never advanced past idle confirmation.
		s.log.Info("idle probe cancelled — user is active, counters resume")
		s.popupActive = false
		s.popupIsProbe = false
		s.popupIsIdle = false
	case ResIdleProbeExpired:
		// 30 s passed with no activity — real idle. Transition from probe to
		// the actual idle break popup, reusing the tier we picked at probe
		// fire. The probe popup already disposed itself; we just need to
		// emit a Show event so the UI opens the break popup.
		s.log.Info("idle probe expired — real idle confirmed, showing break popup", "tier", s.popupTier.String())
		s.popupIsProbe = false
		s.popupIsIdle = true
		// popupActive stays true: we're still showing a popup, just a different one.
		s.events <- s.makeShowEvent(s.popupTier, true)
	}
}

func (s *Scheduler) completeTier(tier Tier) {
	s.log.Info("tier completed", "tier", tier.String())
	s.firedFor[tier] = false
	s.snoozeCount[tier] = 0
	delete(s.pendSnooze, tier)
	switch tier {
	case TierMicro:
		s.microActive = 0
	case TierFull:
		s.fullActive = 0
		if rot := s.cfg.Tiers.Full.Rotation; len(rot) > 0 {
			s.fullRotIdx = (s.fullRotIdx + 1) % len(rot)
		}
	case TierFullRest:
		s.fullRestActive = 0
	}
}

func (s *Scheduler) snoozeTier(tier Tier) {
	s.snoozeCount[tier]++
	s.pendSnooze[tier] = s.cfg.Popup.SnoozeMinutes * 60
	s.log.Info("tier snoozed",
		"tier", tier.String(),
		"snoozeCount", s.snoozeCount[tier],
		"reFireSec", s.pendSnooze[tier])
	// firedFor stays true so threshold won't re-trigger before the snooze fires.
}

func (s *Scheduler) tick() {
	now := time.Now()
	idle := s.act.IdleSeconds()
	working := s.cfg.WithinWorkingHours(now)

	// Daily total of non-idle time. Tracked independently of working hours,
	// pause, and snooze — this is just "calendar-day clock time the user was
	// at the keyboard". Resets on local-midnight rollover.
	today := dayKey(now)
	if s.totalWorkingDay != today {
		s.totalWorkingSec = 0
		s.totalWorkingDay = today
	}
	if idle < s.cfg.IdleResetSeconds {
		s.totalWorkingSec++
	}

	// Working-hours gate: full reset whenever we're outside, EXCEPT we don't
	// touch a popup that was opened via the Test menu (the user explicitly
	// asked to see it; the working-hours auto-close was killing it within a
	// second on every Test outside 9–18).
	if !working {
		if s.popupActive && !s.popupIsTest {
			s.events <- Event{Type: EvtClose}
		}
		if !s.popupIsTest {
			s.fullReset()
		} else {
			s.inIdle = false
		}
		s.updateStatus(working, idle)
		return
	}

	// Pause: hold everything; popup already closed when pause was set.
	if s.paused {
		s.updateStatus(working, idle)
		return
	}

	threshold := s.cfg.IdleResetSeconds
	isIdle := idle >= threshold

	// Idle entry.
	if isIdle && !s.inIdle {
		s.inIdle = true
		s.log.Info("idle entered", "idleSec", idle, "threshold", threshold)
	}

	// Idle return.
	if !isIdle && s.inIdle {
		s.inIdle = false
		s.idleFired = false
		s.log.Info("idle exited", "popupWasIdle", s.popupIsIdle)
		if s.popupIsIdle {
			s.events <- Event{Type: EvtClose}
			s.completeTier(s.popupTier) // idle counted as completion of that tier
			s.popupActive = false
			s.popupIsIdle = false
			s.dequeue()
		}
		// Fall through to tick counters.
	}

	// While idle: counters frozen. Fire the idle probe popup if no popup is
	// showing and we haven't already shown one this idle session. The probe
	// asks "are you really away?" for IdleProbeSeconds before we commit to
	// the real idle break popup — so a user watching a movie at the keyboard
	// (no input but still present) won't have a tier silently reset on them.
	//
	// This branch also re-fires the probe if a regular popup was on screen
	// at idle entry and has since auto-closed during idle, so the user
	// doesn't return to an empty desktop.
	if isIdle {
		if !s.popupActive && !s.idleFired && s.anyTierEnabled() {
			tier := s.nextNearestKind()
			s.popupActive = true
			s.popupIsProbe = true
			s.popupTier = tier // remembered for the break popup after probe expiry
			s.idleFired = true
			s.log.Info("idle probe firing", "tier", tier.String(), "probeSec", s.cfg.IdleProbeSeconds)
			s.events <- s.makeProbeEvent()
		}
		s.updateStatus(working, idle)
		return
	}

	// Normal tick. While ANY tier is in pending-snooze, freeze all break
	// counters: snooze time should "only be calculated in total working
	// time" (which we already incremented at the top of this function).
	// The snoozed-tier popup is still due — its countdown drains below — but
	// the rest of the schedule is paused so the user isn't penalised for
	// snoozing (e.g. a snoozed micro doesn't shorten the gap to the next
	// full break). Disabled tiers also stay frozen — re-enabling resumes
	// from where the counter left off rather than instant-firing.
	snoozing := len(s.pendSnooze) > 0
	if !snoozing {
		if s.cfg.Tiers.Micro.Enabled {
			s.microActive++
		}
		if s.cfg.Tiers.Full.Enabled {
			s.fullActive++
		}
		if s.cfg.Tiers.FullRest.Enabled {
			s.fullRestActive++
		}
	}

	// Drain snooze countdowns. If the tier was disabled mid-snooze (rare —
	// settings save during a pending re-fire), discard the pending snooze
	// rather than firing it.
	for tier, sec := range s.pendSnooze {
		sec--
		if sec <= 0 {
			delete(s.pendSnooze, tier)
			if s.tierEnabled(tier) {
				s.fireOrQueue(tier, false)
			}
		} else {
			s.pendSnooze[tier] = sec
		}
	}

	// Threshold checks per tier. Each is now independent — full and full-rest
	// no longer share a counter; full-rest fires on its own intervalMin.
	if !snoozing {
		if s.cfg.Tiers.Micro.Enabled && !s.firedFor[TierMicro] &&
			s.microActive >= s.cfg.Tiers.Micro.IntervalMin*60 {
			s.firedFor[TierMicro] = true
			s.log.Info("threshold reached", "tier", "micro", "active", s.microActive)
			s.fireOrQueue(TierMicro, false)
		}
		if s.cfg.Tiers.Full.Enabled && !s.firedFor[TierFull] &&
			s.fullActive >= s.cfg.Tiers.Full.IntervalMin*60 {
			s.firedFor[TierFull] = true
			s.log.Info("threshold reached", "tier", "full", "active", s.fullActive)
			s.fireOrQueue(TierFull, false)
		}
		if s.cfg.Tiers.FullRest.Enabled && !s.firedFor[TierFullRest] &&
			s.fullRestActive >= s.cfg.Tiers.FullRest.IntervalMin*60 {
			s.firedFor[TierFullRest] = true
			s.log.Info("threshold reached", "tier", "full-rest", "active", s.fullRestActive)
			s.fireOrQueue(TierFullRest, false)
		}
	}

	s.updateStatus(working, idle)
}

func (s *Scheduler) tierEnabled(t Tier) bool {
	switch t {
	case TierMicro:
		return s.cfg.Tiers.Micro.Enabled
	case TierFull:
		return s.cfg.Tiers.Full.Enabled
	case TierFullRest:
		return s.cfg.Tiers.FullRest.Enabled
	}
	return false
}

func (s *Scheduler) anyTierEnabled() bool {
	return s.cfg.Tiers.Micro.Enabled || s.cfg.Tiers.Full.Enabled || s.cfg.Tiers.FullRest.Enabled
}

// dayKey collapses a local time to YYYYMMDD so we can detect midnight rollover
// with a single integer compare. 0 is reserved for "uninitialised".
func dayKey(t time.Time) int {
	y, m, d := t.Date()
	return y*10000 + int(m)*100 + d
}

func (s *Scheduler) fireOrQueue(tier Tier, idle bool) {
	if s.popupActive {
		s.queue = append(s.queue, queued{tier: tier, idle: idle})
		s.log.Debug("popup queued", "tier", tier.String(), "idle", idle, "queueLen", len(s.queue))
		return
	}
	s.popupActive = true
	s.popupIsIdle = idle
	s.popupTier = tier
	s.log.Debug("popup firing", "tier", tier.String(), "idle", idle)
	s.events <- s.makeShowEvent(tier, idle)
}

func (s *Scheduler) dequeue() {
	if len(s.queue) == 0 {
		return
	}
	next := s.queue[0]
	s.queue = s.queue[1:]
	s.popupActive = true
	s.popupIsIdle = next.idle
	s.popupTier = next.tier
	s.log.Debug("popup dequeued", "tier", next.tier.String(), "idle", next.idle, "queueLen", len(s.queue))
	s.events <- s.makeShowEvent(next.tier, next.idle)
}

func (s *Scheduler) makeShowEvent(tier Tier, idle bool) Event {
	title, img, instr, dur := s.tierContent(tier)
	return Event{
		Type:         EvtShow,
		Tier:         tier,
		Idle:         idle,
		Title:        title,
		ImagePath:    img,
		Instructions: instr,
		Duration:     dur,
		SnoozeCount:  s.snoozeCount[tier],
	}
}

// makeProbeEvent builds the "are you still there?" idle probe popup that
// precedes the real idle break popup. Tier is recorded only so the scheduler
// remembers which break to fire once the probe expires; the probe popup itself
// is generic and tier-agnostic.
func (s *Scheduler) makeProbeEvent() Event {
	sec := s.cfg.IdleProbeSeconds
	if sec <= 0 {
		sec = 30
	}
	return Event{
		Type:         EvtShow,
		Tier:         s.popupTier,
		Idle:         false,
		IsProbe:      true,
		Title:        "Are you still there?",
		ImagePath:    "",
		Instructions: "Move the mouse if you're at the keyboard. Otherwise a break reminder will appear.",
		Duration:     time.Duration(sec) * time.Second,
	}
}

func (s *Scheduler) tierContent(tier Tier) (title, imgPath, instr string, dur time.Duration) {
	switch tier {
	case TierMicro:
		t := s.cfg.Tiers.Micro
		return "Eye Break", resolveImage(t.Image), t.Instructions, time.Duration(t.DurationSec) * time.Second
	case TierFull:
		t := s.cfg.Tiers.Full
		rot := t.Rotation
		if len(rot) == 0 {
			return "Stretch Break", "", "Stand and stretch.", time.Duration(t.DurationSec) * time.Second
		}
		idx := s.fullRotIdx % len(rot)
		item := rot[idx]
		return "Stretch Break", resolveImage(item.Image), item.Instructions, time.Duration(t.DurationSec) * time.Second
	case TierFullRest:
		t := s.cfg.Tiers.FullRest
		return "Long Rest", resolveImage(t.Image), t.Instructions, time.Duration(t.DurationSec) * time.Second
	}
	return "Break", "", "", 30 * time.Second
}

// nextNearestKind picks the enabled tier whose counter is closest to firing.
// Used for idle and Test commands. If no tier is enabled, falls back to
// TierMicro so callers always get a deterministic answer (Test, in that
// degenerate case, would still pop a micro reminder).
func (s *Scheduler) nextNearestKind() Tier {
	type cand struct {
		tier Tier
		rem  int
	}
	var best cand
	have := false
	consider := func(t Tier, rem int) {
		if !have || rem < best.rem {
			best = cand{t, rem}
			have = true
		}
	}
	if s.cfg.Tiers.Micro.Enabled {
		consider(TierMicro, s.cfg.Tiers.Micro.IntervalMin*60-s.microActive)
	}
	if s.cfg.Tiers.Full.Enabled {
		consider(TierFull, s.cfg.Tiers.Full.IntervalMin*60-s.fullActive)
	}
	if s.cfg.Tiers.FullRest.Enabled {
		consider(TierFullRest, s.cfg.Tiers.FullRest.IntervalMin*60-s.fullRestActive)
	}
	if !have {
		return TierMicro
	}
	return best.tier
}

func (s *Scheduler) fullReset() {
	s.microActive = 0
	s.fullActive = 0
	s.fullRestActive = 0
	s.fullRotIdx = 0
	s.firedFor = map[Tier]bool{}
	s.snoozeCount = map[Tier]int{}
	s.pendSnooze = map[Tier]int{}
	s.popupActive = false
	s.popupIsIdle = false
	s.popupIsProbe = false
	s.popupIsTest = false
	s.queue = nil
	s.inIdle = false
	s.idleFired = false
}

func (s *Scheduler) updateStatus(working bool, idle int) {
	microRem := max0(s.cfg.Tiers.Micro.IntervalMin*60 - s.microActive)
	fullRem := max0(s.cfg.Tiers.Full.IntervalMin*60 - s.fullActive)
	fullRestRem := max0(s.cfg.Tiers.FullRest.IntervalMin*60 - s.fullRestActive)
	nearest := s.nextNearestKind()

	// Pick the snooze closest to expiring (smallest remaining). In practice
	// only one popup at a time can be snoozed, so this map is usually 0 or 1
	// entries — but the code stays correct if multiple ever queue up.
	var (
		snoozeActive    bool
		snoozeTier      Tier
		snoozeRemaining int
	)
	for tier, rem := range s.pendSnooze {
		if !snoozeActive || rem < snoozeRemaining {
			snoozeActive = true
			snoozeTier = tier
			snoozeRemaining = rem
		}
	}
	snoozeTotal := s.cfg.Popup.SnoozeMinutes * 60
	snoozeElapsed := snoozeTotal - snoozeRemaining
	if snoozeElapsed < 0 {
		snoozeElapsed = 0
	}

	st := Status{
		Paused:            s.paused,
		InWorkingHours:    working,
		IdleSec:           idle,
		MicroEnabled:      s.cfg.Tiers.Micro.Enabled,
		FullEnabled:       s.cfg.Tiers.Full.Enabled,
		FullRestEnabled:   s.cfg.Tiers.FullRest.Enabled,
		MicroRemaining:    microRem,
		FullRemaining:     fullRem,
		FullRestRemaining: fullRestRem,
		FullActive:        s.fullActive,
		NextNearestKind:   nearest,
		AnyEnabled:        s.anyTierEnabled(),
		PopupActive:       s.popupActive,
		SnoozeActive:      snoozeActive,
		SnoozeTier:        snoozeTier,
		SnoozeElapsed:     snoozeElapsed,
		SnoozeTotal:       snoozeTotal,
		TotalWorkingSec:   s.totalWorkingSec,
	}
	s.statusMu.Lock()
	s.status = st
	s.statusMu.Unlock()
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// resolveImage turns a bare filename like "eye_2020.jpg" into an absolute
// path under <dataDir>/assets/exercises/. Returns "" if the input is empty.
// (The popup will show text-only if the file is missing on disk.)
func resolveImage(name string) string {
	if name == "" {
		return ""
	}
	return filepath.Join(config.DataDir(), "assets", "exercises", name)
}

// Dump returns a human-readable snapshot useful for debug logging.
func (s *Scheduler) Dump() string {
	st := s.Snapshot()
	return fmt.Sprintf("paused=%v working=%v idle=%ds micro=%ds full=%ds fullRest=%ds nearest=%s popup=%v",
		st.Paused, st.InWorkingHours, st.IdleSec,
		st.MicroRemaining, st.FullRemaining, st.FullRestRemaining,
		st.NextNearestKind, st.PopupActive)
}
