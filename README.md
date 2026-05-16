# Limber

A tiny, portable system-tray app that nudges you to stretch — adaptively, based on **actual** mouse/keyboard activity, following evidence-based ergonomic guidance.

> **Status:** Windows v1 implemented. See [Build & run](#build--run) below.

## Targets

- **Primary: Windows 11** (Home edition).
- **Secondary: Ubuntu 24.04 with XFCE.** XFCE defaults to **X11**, so the standard idle API works (no Wayland complications).
- Single portable binary per OS. No installer, no admin rights, no runtime dependencies, no database, no telemetry, no network calls after first install.

## What makes Limber different

1. **Three break tiers, not user-picked exercises.** The schedule follows orthopedic guidance; the user does not pick exercises. Limber rotates through anatomically-targeted stretches automatically.
2. **Active-time accounting.** Counters tick only while you're actually typing/clicking. Step away → they pause.
3. **Idle = next break.** If you go idle ≥ 5 min, Limber assumes you've stepped away (presumably moving), and the *next-due* break popup appears so you see the recommended stretch when you return. The popup waits — no auto-close — until your first mouse/keyboard event. If a regular popup happened to be on screen when you went idle and its countdown ran out while you were away, the idle popup still appears so you don't return to an empty desktop.
4. **Frictionless dismissal.** No buttons. The whole left or right strip of the popup is a hover zone — push the cursor toward it and it triggers.
5. **Pause for screen recordings.** A toggleable Pause menu freezes everything; never persists across restarts.

## Tech stack

- **Language:** Go (≥ 1.22)
- **GUI:** [`lxn/walk`](https://github.com/lxn/walk) — pure-Go Win32 wrapper. **No CGo, no MinGW required.** Native menus, NotifyIcon, Custom-painted popup window.
- **Idle detection:**
  - Windows: `user32!GetLastInputInfo` via `syscall` (pure Go).
  - Linux: stub (returns 0). Add later via `libXss` for XFCE.
- **No-focus popup (Windows):** `WS_EX_NOACTIVATE` + `WS_EX_TOOLWINDOW` + `SW_SHOWNOACTIVATE` so typing in VS Code is never interrupted.
- **Autorun:**
  - Windows: registry value at `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
  - Linux: `~/.config/autostart/limber.desktop` (file already implemented; pending Linux UI port to be useful)
- **Config:** single `config.json` next to the binary (portable). Pause state is **runtime-only** and never written.

## The break model

Three counters run in parallel, all paused outside working hours, while idle, while Pause is on, or while any tier is in a pending snooze (a snooze delays *all* future break events, not just the snoozed tier — see [Hover-trigger strips](#hover-trigger-strips-no-buttons-no-clicks)):

| Tier          | Default interval                        | Default duration | Auto-close on countdown 0?           | Content                                                 |
| ------------- | --------------------------------------- | ---------------- | ------------------------------------ | ------------------------------------------------------- |
| **Micro**     | every **20 min** active                 | **20 sec**       | yes                                  | 20-20-20 eye break                                      |
| **Full**      | every **30 min** active                 | **60 sec**       | yes                                  | rotates: neck → shoulders → wrists → lumbar → hip flexor |
| **Full-rest** | every **90 min** active                 | **15 min**       | yes                                  | stand, walk, longer stretches                           |

Each tier can be independently enabled or disabled in Settings. Disabled tiers are skipped — their counter is frozen and they never fire popups. Rotation uses a runtime-only round-robin index per tier (lost on restart, which is fine — orthopedically the order doesn't matter).

### Idle handling (the critical rule)

Idle is treated in two stages: a **probe** to disambiguate "user is watching a movie at the keyboard" from "user walked away", then the real **break** popup once we're confident the user is away.

1. `idle ≥ idleResetSeconds` (default **300 s** = 5 min) → all counters **pause**, and Limber shows the **idle probe** popup. The probe:
   - is generic ("Are you still there?" — no exercise image),
   - paints **both** edge strips as `CLOSE` (red); there is no snooze option,
   - runs a `idleProbeSeconds` countdown (default **30 s**),
   - is dismissed by **any** mouse/keyboard activity (the strip dwell is bypassed — the first real mouse move cancels it). A probe cancellation is treated as "user was here all along": no tier completes, counters resume from their pre-idle values.
2. If the probe countdown reaches 0 with no activity, the user really is away. Limber:
   - closes the probe,
   - opens the actual **idle break** popup for the **next-nearest-due** enabled tier (image + instructions of that tier),
   - does **not** auto-close when this break countdown reaches 0 — it keeps waiting,
   - **closes the instant** any mouse movement or keyboard activity is detected.
3. On return from a real idle (probe → break), the shown tier's counter resets to 0 (treated as completed); other tiers' counters resume from their paused values.

If a regular popup is already on screen at the moment idle starts, the probe is deferred. If that regular popup auto-completes while you're still idle, the probe fires then so you always have something to dismiss on return.

### Reset menu vs. idle

- **Idle return** → only the shown tier resets; other counters keep their accumulated active-time.
- **Reset menu click** → full reset of every counter and every rotation index, equivalent to relaunching the app.

### Outside working hours

If now is not in `[workingHours.start, workingHours.end]`, all counters are held at 0 (full reset). When the clock hits `workingHours.start` we begin a fresh day.

## Tray menu

Right-click only:

- **Pause** *(checkmark, off at startup, never saved)* — when on, all counters are frozen; popups suppressed; tray icon dims. Toggling off resumes from the same counter values. Any popup that was on screen (and any queued behind it) is preserved and re-fired on unpause, so a screen-recording pause doesn't make you lose a stretch reminder.
- *— separator —*
- **Reset** — full reset of all counters (same as relaunching).
- **Test** — fast-forwards the next-due tier so its popup appears immediately. Use this when you stand up to stretch on your own and want to see/dismiss the popup before the timer reaches it.
- **Settings…** — opens the settings window with `Default` / `Save` / `Cancel`.
- *— separator —*
- **Exit**

## Popup behaviour

### Window properties

- Borderless, fixed-size, **never takes focus**, no taskbar entry.
- Dark background (`#1e1e1e`), white text.
- Default size: **200 × 100** (compact, designed to sit unobtrusively in a screen corner). Increase `popup.width` / `popup.height` in Settings if you want room for a larger image; the popup auto-scales the layout to the configured pixel size.
- Positioned in the configured screen corner with `horizontalPaddingPx` / `verticalPaddingPx` margins (both default to 0 — the popup sits flush with the screen edge).
- Only **one** popup at a time. If a second tier comes due while one is showing, it queues and fires after the current one closes.

### Layout, top to bottom

1. Exercise image (centered)
2. Instruction text (1–2 lines)
3. **Snooze indicator** — only visible when `snoozeCount ≥ 1`, e.g. *"2nd snooze"* in a slightly warmer tint to nudge the user's awareness.
4. Countdown `M:SS` (regular) or `M:SS — waiting for activity…` (idle-triggered).

### Hover-trigger strips (no buttons, no clicks)

The strips run the **full height** of the window, `edgeTriggerPx` wide (default 30).

- **Close strip** — light-red background (`#f4a8a8`), label `C L O S E` written vertically in white. Action: dismiss popup, reset that tier's counter (treated as completed).
- **Snooze strip** — light-yellow background (`#f5e6a3`), label `S N O O Z E` written vertically in dark gray. Action: dismiss popup; the same tier re-fires after `snoozeMinutes` (default 3, configurable, **unlimited** repeats). The snooze counter increments; the next popup will display `Nth snooze` below the image. **While any tier is in pending snooze, every other tier's counter freezes too** — snoozing one break delays all future events by the snooze interval, rather than only postponing the snoozed tier.

For idle popups, the strip dwell is bypassed: any real mouse move (after Windows' initial seed event) closes the popup as a completion — matching the "closes the instant any activity is detected" contract and removing the race where a cursor entering the snooze strip on return could accidentally snooze the popup.

#### Strip placement is mirrored across the screen

The snooze strip is always on the side of the popup **opposite** to the screen edge it's docked against:

| Popup corner   | Close strip (toward screen edge) | Snooze strip (away from screen edge) |
| -------------- | -------------------------------- | ------------------------------------- |
| `bottom-left`  | left side of popup               | right side of popup                   |
| `top-left`     | left side of popup               | right side of popup                   |
| `bottom-right` | right side of popup              | left side of popup                    |
| `top-right`    | right side of popup              | left side of popup                    |

Rationale: pushing the cursor "toward the corner" commits (close); flicking it "into the screen" snoozes. Same muscle memory regardless of which corner you've chosen.

#### Dwell guard

`edgeDwellMs` (default **0 ms**, configurable) is the time the cursor must remain in the strip before triggering. Set above zero to prevent accidental fires when the cursor merely passes through; at the default 0 the strip fires the moment the cursor crosses into it (with the seed-event guard preventing auto-fire if the popup opens under the cursor).

### Countdown end behaviour

- **Regular popup** → on `0:00`, auto-close, reset that tier's counter (treated as completed).
- **Idle-triggered popup** → on `0:00`, do **nothing**; keep showing until activity is detected, then close.

### Audio

- `audio.enabled` default **false**.
- When enabled, plays a short, soft chime once on popup appear. No sound on snooze/close.
- Bundled WAV in `assets/sounds/chime.wav`.

## Settings

All values are stored in `config.json` next to the binary. The settings window mirrors the schema; `Default` resets the **form** to built-ins (does not save until `Save` is clicked); `Cancel` discards form edits.

```jsonc
{
  "workingHours": {
    "start": "09:00",
    "end":   "18:00"
  },
  "idleResetSeconds": 300,
  "idleProbeSeconds": 30,
  "popup": {
    "corner":              "bottom-left", // top-left | top-right | bottom-left | bottom-right
    "width":               200,
    "height":              100,
    "horizontalPaddingPx": 0,
    "verticalPaddingPx":   0,
    "edgeTriggerPx":       30,
    "edgeDwellMs":         0,
    "snoozeMinutes":       3
  },
  "audio": {
    "enabled": false
  },
  "startAtBoot": false,
  "logLevel":    "info", // info | debug | error | off

  "tiers": {
    "micro": {
      "enabled":             true,
      "intervalMin":         20,
      "durationSec":         20,
      "image":               "eye_2020.jpg",
      "instructions":        "Look at something at least 20 feet (6 m) away for 20 seconds."
    },
    "full": {
      "enabled":             true,
      "intervalMin":         30,
      "durationSec":         60,
      "rotation": [
        { "id": "chin-tuck",        "image": "cervical_retraction.jpg",  "instructions": "Sit tall. Pull your chin straight back (double-chin). Hold 5 s. Repeat 10×." },
        { "id": "shoulder-rolls",   "image": "scapular_retraction.jpg",  "instructions": "Roll shoulders back 10×. Then squeeze shoulder blades together, hold 5 s, repeat 10×." },
        { "id": "wrist-stretch",    "image": "wrist_flexor_stretch.jpg", "instructions": "Arm out, palm up. Pull fingers down with the other hand. Hold 20 s each side, both directions." },
        { "id": "lumbar-extension", "image": "lumbar_extension.jpg",     "instructions": "Stand. Hands on lower back. Gently arch backward. Hold 5 s. Repeat 10×." },
        { "id": "hip-flexor",       "image": "hip_flexor_stretch.jpg",   "instructions": "Step one foot back into a lunge. Tuck pelvis. Hold 30 s each side." }
      ]
    },
    "fullRest": {
      "enabled":             true,
      "intervalMin":         90,
      "durationSec":         900,
      "image":               "walk_break.jpg",
      "instructions":        "Stand up, walk for several minutes, look around the room, do a full-body stretch."
    }
  }
}
```

Full-rest is an independent counter on its own `intervalMin`, not a substitution for every Nth Full break — disabling Full does not affect when Full-rest fires.

### Settings UI

The settings window has tabs: **General**, **Popup**, **Micro**, **Full**, **Full-rest**, **Audio**.

- **General** — working hours, idle-reset seconds, idle-probe seconds, start-at-boot, log level.
- **Popup** — corner, width, height, horizontal/vertical padding, edge-trigger px, edge-dwell ms, snooze minutes.
- **Micro / Full / Full-rest** — enable toggle, interval, duration, instructions, **image listbox** populated from any `.jpg` in `assets/exercises/`.
- **Full** also lets you reorder / enable / disable individual rotation entries (read-only in v1 — edit `config.json` directly).
- **Audio** — enable + (later) volume.

Buttons (always at the bottom): **Default**, **Save**, **Cancel**.

- *Default* — repopulates the form from compiled-in defaults; does not write to disk.
- *Save* — validates, writes `config.json`, applies live (no restart needed). If `startAtBoot` changed, also writes/removes the OS autorun entry.
- *Cancel* — closes without saving.

## Activity detection

A 1-second ticker queries system-wide idle time:

- **Windows:** `GetLastInputInfo` (returns ticks since last input across all processes — no hooks, no privileges).
- **Linux/X11:** `XScreenSaverQueryInfo` from `libXss`.

Per-tier loop, every second (simplified):

```
update daily total active-time counter

if outsideWorkingHours:
    close non-Test popup; full reset of counters / rotations / snooze
    return

if pause:
    counters frozen; current popup (if any) was closed when pause engaged
    and pushed onto the front of the queue for re-fire on unpause
    return

if idle >= idleResetSeconds:
    counters frozen
    if no popup is showing AND no idle popup has fired this idle session
       AND at least one tier is enabled:
        fire IDLE PROBE popup ("are you still there?", 30 s, no snooze)
    # probe → ResIdleProbeCancelled: counters resume (handled on idle exit)
    # probe → ResIdleProbeExpired:   close probe, open idle break popup
    return

# Active state.
on activity-after-idle:
    close any IDLE popup; reset that tier's counter; advance rotation

if any tier is in pending snooze:
    all break counters frozen (snooze delays every future event)
else:
    for each enabled tier: counter += 1 sec

drain pending-snooze timers; when one expires, re-fire that tier
for each enabled tier where counter >= intervalMin*60 and not already fired:
    queue popup (or fire immediately if none showing)
```

## Project layout

```
stretchly/                        (working dir; module name is "limber")
├── main.go                       # entry, lifecycle
├── go.mod
├── config/
│   └── config.go                 # load/save config.json + defaults
├── activity/
│   ├── activity.go               # cross-platform interface
│   ├── activity_windows.go       # GetLastInputInfo (pure Go syscall)
│   └── activity_linux.go         # stub (returns 0)
├── scheduler/
│   └── scheduler.go              # tier counters, rotation, queue, idle/pause
├── autostart/
│   ├── autostart.go
│   ├── autostart_windows.go      # HKCU\…\Run registry value
│   └── autostart_linux.go        # ~/.config/autostart/*.desktop
├── audio/
│   ├── audio_windows.go          # PlaySoundW (system chime)
│   └── audio_linux.go            # no-op
├── ui/                           # Windows-only in v1
│   ├── app_windows.go            # App struct, MainWindow lifecycle, event loop
│   ├── tray_windows.go           # NotifyIcon + tray context menu
│   ├── popup_windows.go          # borderless no-activate popup, edge strips, countdown
│   ├── settings_windows.go       # tabbed settings dialog (default/save/cancel)
│   └── icons.go                  # programmatic tray icon (active + paused)
├── assets/
│   └── exercises/
│       ├── LICENSES.md           # provenance table; drop your JPGs in here
│       └── *.jpg                 # exercise illustrations (user-supplied)
└── config.json                   # generated on first run beside the .exe
```

## Evidence-based defaults

| Source                                                            | Recommendation                                              |
| ----------------------------------------------------------------- | ----------------------------------------------------------- |
| American Optometric Association                                   | **20-20-20 rule** for digital eye strain                    |
| AAOS (American Academy of Orthopaedic Surgeons) — *OrthoInfo*     | Cervical & shoulder micro-breaks every ~30 min              |
| OSHA Computer Workstations eTool                                  | Brief pauses every 20–30 min; longer break each hour        |
| ACOEM Office Ergonomics guidance                                  | Posture changes & stretches every 30 min                    |
| NIOSH WMSDs guidance                                              | Limit static postures; standing/walking break each hour     |
| Mayo Clinic — Office Ergonomics                                   | Lumbar extension & hip-flexor stretches for prolonged sitting |

Image assets will use **public-domain or CC-licensed** illustrations only (e.g. NIH, OSHA publications, Wikimedia line drawings). Provenance and license recorded per file in `assets/exercises/LICENSES.md`. No copyrighted clinical images redistributed.

## Tray icon

A simple SVG will be generated and exported to PNG at the sizes Windows and XFCE need (16, 24, 32, 48). Two states:
- **Active** — full-color glyph (a stylized stretching figure or arrow loop).
- **Paused** — same glyph at 40 % opacity with a small pause-bar overlay.

## Build & run

### Prerequisites

- **Go 1.22 or newer.** Download from <https://go.dev/dl/> (use the MSI on Windows). After install, open a fresh terminal and confirm `go version` works.
- **No C compiler needed.** `walk` is pure Go.

### Build (Windows)

From the project root (`e:\www\stretchly`):

```bash
# 1. Fetch dependencies (first time only)
go mod tidy

# 2. (First time only) generate the application manifest .syso so Windows 11
#    enables Common Controls v6 and walk's tooltips initialise correctly.
go install github.com/akavel/rsrc@latest
"$HOME/go/bin/rsrc.exe" -manifest limber.manifest -o rsrc.syso

# 3. Build
go build -ldflags="-H windowsgui -s -w" -o limber.exe .
```

`rsrc.syso` is automatically included by Go in any subsequent build (the `.syso` extension is special-cased), so steps 1 and 2 only run once. Re-run them only if you change `limber.manifest`.

The `-H windowsgui` flag prevents a console window from appearing. `-s -w` strips debug info to keep the binary small.

For development you can run directly without producing an exe:

```bash
go run .
```

This shows logs in the terminal — useful for debugging. Stop it with Ctrl-C (or right-click the tray icon and Exit).

### First run

```bash
./limber.exe
```

- A tray icon appears (figure with arms outstretched).
- `config.json` is created in the project folder with default values.
- A `limber.log` file is created beside the .exe for diagnostics.
- Right-click the tray icon to access **Pause / Reset / Test / Settings… / Exit**.

### Adding exercise images

Drop JPGs into `assets/exercises/` with the names listed in `LICENSES.md` (or pick any names and adjust `config.json` / the image fields in **Settings**). If a referenced image is missing, the popup falls back to text-only — the app still works.

### Uninstall

Untick *Start at boot* in **Settings** (so the autorun entry is removed), then delete the folder. No installer, no leftover registry keys aside from that one autorun entry.

## What's NOT done in v1

- **Linux UI.** `walk` is Windows-only. The non-UI packages (`config`, `scheduler`, `activity`, `autostart`, `audio`) already use build tags and have Linux stubs ready. To port: add a `ui_linux.go` using GTK or another Linux toolkit.
- **Manifest / Common Controls v6 visuals.** Without a Win32 application manifest, controls render in a slightly older (but functional) style. Add a `manifest.xml` + `rsrc` step later for modern visuals.
- **Multi-monitor popup placement.** Always uses the primary display's work area.
- **Inline rotation editing.** The Full-break tab shows the rotation list read-only; edit `config.json` directly to add / remove entries.
- **Bundled exercise illustrations.** Source and add free-licensed JPGs separately.

## License

[MIT](LICENSE) — do whatever you want with the code, including selling forks. Bundled exercise images in `assets/exercises/` are public-domain or CC-licensed; provenance per file is in `assets/exercises/LICENSES.md`.

## Known caveats

- **Test outside working hours**: Test-triggered popups are flagged so the working-hours gate won't auto-close them — you can preview a break popup at any time without the next tick killing it.
- **Concurrent settings edits**: changing settings while a popup is on screen is harmless but the in-flight popup uses the values it captured at open time.
- **High-DPI**: popup geometry in `config.json` is in physical pixels. On 200 % scaled displays the default 200 × 100 will look small; bump the values in **Settings**.
