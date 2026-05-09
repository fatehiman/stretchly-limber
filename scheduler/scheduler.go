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
	Title        string // display title (e.g. "Eye Break")
	ImagePath    string // absolute path to image file, may be "" if missing
	Instructions string
	Duration     time.Duration // popup countdown duration
	SnoozeCount  int           // 0 = first showing, 1 = first snooze, ...
}

// ResultAction is what the user did with a popup.
type ResultAction int

const (
	ResCompleted ResultAction = iota // close strip, or countdown reached 0 (regular popup)
	ResSnoozed                       // snooze strip
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
	microActive int
	fullActive  int
	fullCount   int
	fullRotIdx  int

	firedFor    map[Tier]bool
	snoozeCount map[Tier]int
	pendSnooze  map[Tier]int

	paused      bool
	inIdle      bool
	popupActive bool
	popupTier   Tier
	popupIsIdle bool
	popupIsTest bool // current popup was triggered by Test menu — survives the working-hours gate
	queue       []queued

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
	Paused          bool
	InWorkingHours  bool
	IdleSec         int
	MicroRemaining  int
	FullRemaining   int
	FullActive      int  // seconds of active time since last full / full-rest break completed
	NextFullKind    Tier // TierFull or TierFullRest — what would fire next at the full threshold
	NextNearestKind Tier // tier whose counter is closest to firing (overall next break)
	PopupActive     bool

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
		s.popupIsTest = true
		s.popupTier = tier
		s.events <- s.makeShowEvent(tier, false)
	case cmdPauseOn:
		s.log.Info("pause on")
		s.paused = true
		s.events <- Event{Type: EvtClose}
		s.popupActive = false
		s.popupIsIdle = false
		s.popupIsTest = false
		s.queue = nil
	case cmdPauseOff:
		s.log.Info("pause off")
		s.paused = false
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
	case ResSnoozed:
		s.snoozeTier(tier)
	}
	s.popupActive = false
	s.popupIsIdle = false
	s.popupIsTest = false
	s.dequeue()
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
		s.fullCount++
		if rot := s.cfg.Tiers.Full.Rotation; len(rot) > 0 {
			s.fullRotIdx = (s.fullRotIdx + 1) % len(rot)
		}
	case TierFullRest:
		s.fullActive = 0
		s.fullCount++
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
		// Fire idle popup if no popup is showing.
		if !s.popupActive {
			tier := s.nextNearestKind()
			s.popupActive = true
			s.popupIsIdle = true
			s.popupTier = tier
			s.log.Info("idle popup firing", "tier", tier.String())
			s.events <- s.makeShowEvent(tier, true)
		}
		s.updateStatus(working, idle)
		return
	}

	// Idle return.
	if !isIdle && s.inIdle {
		s.inIdle = false
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

	// Still idle but popup already shown — counters frozen.
	if isIdle {
		s.updateStatus(working, idle)
		return
	}

	// Normal tick. While ANY tier is in pending-snooze, freeze all break
	// counters: snooze time should "only be calculated in total working
	// time" (which we already incremented at the top of this function).
	// The snoozed-tier popup is still due — its countdown drains below — but
	// the rest of the schedule is paused so the user isn't penalised for
	// snoozing (e.g. a snoozed micro doesn't shorten the gap to the next
	// full break).
	snoozing := len(s.pendSnooze) > 0
	if !snoozing {
		s.microActive++
		s.fullActive++
	}

	// Drain snooze countdowns.
	for tier, sec := range s.pendSnooze {
		sec--
		if sec <= 0 {
			delete(s.pendSnooze, tier)
			s.fireOrQueue(tier, false)
		} else {
			s.pendSnooze[tier] = sec
		}
	}

	// Threshold checks. Skip while snoozing — no counter advanced this tick,
	// so nothing new can have crossed a threshold anyway, but the explicit
	// guard keeps the intent obvious.
	if !snoozing {
		if !s.firedFor[TierMicro] && s.microActive >= s.cfg.Tiers.Micro.IntervalMin*60 {
			s.firedFor[TierMicro] = true
			s.log.Info("threshold reached", "tier", "micro", "active", s.microActive)
			s.fireOrQueue(TierMicro, false)
		}
		if !s.firedFor[TierFull] && !s.firedFor[TierFullRest] &&
			s.fullActive >= s.cfg.Tiers.Full.IntervalMin*60 {
			kind := s.nextFullKind()
			s.firedFor[kind] = true
			s.log.Info("threshold reached", "tier", kind.String(), "active", s.fullActive, "fullCount", s.fullCount)
			s.fireOrQueue(kind, false)
		}
	}

	s.updateStatus(working, idle)
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

// nextNearestKind picks the tier whose counter is closest to firing.
// Used for idle and Test commands.
func (s *Scheduler) nextNearestKind() Tier {
	microRem := s.cfg.Tiers.Micro.IntervalMin*60 - s.microActive
	fullRem := s.cfg.Tiers.Full.IntervalMin*60 - s.fullActive
	if microRem <= fullRem {
		return TierMicro
	}
	return s.nextFullKind()
}

// nextFullKind decides whether the next full-tier firing would be a regular
// Full or a FullRest, based on the running count of completed full breaks.
func (s *Scheduler) nextFullKind() Tier {
	every := s.cfg.Tiers.FullRest.EveryNthFull
	if every <= 0 {
		every = 3
	}
	if (s.fullCount+1)%every == 0 {
		return TierFullRest
	}
	return TierFull
}

func (s *Scheduler) fullReset() {
	s.microActive = 0
	s.fullActive = 0
	s.fullCount = 0
	s.fullRotIdx = 0
	s.firedFor = map[Tier]bool{}
	s.snoozeCount = map[Tier]int{}
	s.pendSnooze = map[Tier]int{}
	s.popupActive = false
	s.popupIsIdle = false
	s.popupIsTest = false
	s.queue = nil
	s.inIdle = false
}

func (s *Scheduler) updateStatus(working bool, idle int) {
	microRem := max0(s.cfg.Tiers.Micro.IntervalMin*60 - s.microActive)
	fullRem := max0(s.cfg.Tiers.Full.IntervalMin*60 - s.fullActive)
	nextFull := s.nextFullKind()
	nearest := TierMicro
	if microRem > fullRem {
		nearest = nextFull
	}

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
		Paused:          s.paused,
		InWorkingHours:  working,
		IdleSec:         idle,
		MicroRemaining:  microRem,
		FullRemaining:   fullRem,
		FullActive:      s.fullActive,
		NextFullKind:    nextFull,
		NextNearestKind: nearest,
		PopupActive:     s.popupActive,
		SnoozeActive:    snoozeActive,
		SnoozeTier:      snoozeTier,
		SnoozeElapsed:   snoozeElapsed,
		SnoozeTotal:     snoozeTotal,
		TotalWorkingSec: s.totalWorkingSec,
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
	return fmt.Sprintf("paused=%v working=%v idle=%ds micro=%ds full=%ds nextFull=%s popup=%v",
		st.Paused, st.InWorkingHours, st.IdleSec, st.MicroRemaining, st.FullRemaining, st.NextFullKind, st.PopupActive)
}
