// Package lcd drives the front display (2 lines × 16 characters).
//
// 32 characters is very little, so what to show is decided by priority
// tiers: the first tier with something to say wins, and its screens rotate
// every 5 seconds:
//
//	0  startup / shutdown            "Starting..."
//	1  critical problems (blinking)  "DISK FAILING" / "Replace bay 3"
//	2  network change to confirm     "Confirm network" / "rollback in 75s"
//	3  power cut                     "ON UPS BATTERY" / "64%  12 min left"
//	4  what you just asked for       "Backup 1/2..." / "Locate:"
//	5  long operations               "Rebuild data" / "42%  ~3h10"
//	6  warnings                      "DISK WARNING" / "Bay 2 (sdb)"
//	7  routine information           name + IP, capacity, temperatures, uptime
//
// The backlight is on for tiers 0-6, and for routine information only
// during 2 minutes after a button press (or always, if configured).
//
// No personal information is shown (no user or file names): the display
// is visible to anyone walking past.
package lcd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

const Width = 16

// Snapshot is the state the display is computed from, refreshed every 5 s.
type Snapshot struct {
	Hostname   string
	IP         string
	Problems   []Problem
	Volumes    []Volume
	CPUTemp    int // °C, 0 = unknown
	DiskTemp   int // hottest disk, °C
	FanRPM     int // -1 = no fan sensor
	Uptime     time.Duration
	UPS        UPS
	NetPending time.Time // zero = none
	Locating   []string  // "Bay 3"
	Button     Button
	Job        string // title of the running background job, "" if none
	PowerOff   string // "23:00" when a power schedule is set
	PowerOn    string
}

type Problem struct {
	Severity string // critical, warning
	Kind     string
	Where    string // "Bay 3", volume name...
	Detail   string // short, e.g. "42%"
}

type Volume struct {
	Name       string
	UsedPct    int
	FreeBytes  int64
	SyncAction string // "" when idle
	SyncPct    float64
	ETA        time.Duration
}

type UPS struct {
	OnBattery  bool
	Charge     int
	RuntimeMin int
}

type Button struct {
	Running    bool
	Cur, Total int
}

// Frame is what the display shows at one moment.
type Frame struct {
	Line1     string `json:"line1"`
	Line2     string `json:"line2"`
	Backlight bool   `json:"backlight"`
	Blink     bool   `json:"blink"`
	Tier      string `json:"tier"`
}

// Output is the physical display (nil-safe: Simulated does nothing).
type Output interface {
	Show(line1, line2 string, backlight bool) error
	Name() string
}

type Engine struct {
	Collect  func(ctx context.Context) Snapshot
	Out      Output
	Now      func() time.Time
	AlwaysOn func() bool

	mu        sync.Mutex
	snap      Snapshot
	phase     [2]string
	wakeUntil time.Time
	// notice: a message shown above everything but startup/shutdown until
	// noticeUntil (a new admin password, "keep holding" while resetting).
	notice      [2]string
	noticeUntil time.Time
	frame     Frame
	shown     Frame
}

func New(collect func(context.Context) Snapshot, out Output) *Engine {
	return &Engine{Collect: collect, Out: out, Now: time.Now, AlwaysOn: func() bool { return false }}
}

// Notice shows a message for d, above everything but startup/shutdown,
// with the backlight on. A zero d clears it.
func (e *Engine) Notice(line1, line2 string, d time.Duration) {
	e.mu.Lock()
	e.notice, e.noticeUntil = [2]string{line1, line2}, e.Now().Add(d)
	e.mu.Unlock()
	e.render()
}

// SetPhase shows a startup/shutdown message above everything ("" clears it).
func (e *Engine) SetPhase(line1, line2 string) {
	e.mu.Lock()
	e.phase = [2]string{line1, line2}
	e.mu.Unlock()
	e.render()
}

// Wake turns the backlight on for 2 minutes (button press, startup).
func (e *Engine) Wake() {
	e.mu.Lock()
	e.wakeUntil = e.Now().Add(2 * time.Minute)
	e.mu.Unlock()
	e.render()
}

func (e *Engine) Current() Frame {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.frame
}

// Run refreshes the snapshot every 5 s and the display every second.
func (e *Engine) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for n := 0; ; n++ {
		if n%5 == 0 && e.Collect != nil {
			snap := e.Collect(ctx)
			e.mu.Lock()
			e.snap = snap
			e.mu.Unlock()
		}
		e.render()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (e *Engine) render() {
	e.mu.Lock()
	f := e.compute(e.Now())
	e.frame = f
	changed := f.Line1 != e.shown.Line1 || f.Line2 != e.shown.Line2 || f.Backlight != e.shown.Backlight
	e.shown = f
	e.mu.Unlock()
	if changed && e.Out != nil {
		e.Out.Show(f.Line1, f.Line2, f.Backlight)
	}
}

type screen [2]string

// compute picks the screen for time now (caller holds e.mu).
func (e *Engine) compute(now time.Time) Frame {
	s := e.snap
	tiers := []struct {
		name    string
		screens []screen
	}{
		{"phase", phaseScreens(e.phase)},
		{"notice", e.noticeScreens(now)},
		{"critical", problemScreens(s, "critical")},
		{"network", networkScreens(s, now)},
		{"ups", upsScreens(s)},
		{"feedback", feedbackScreens(s)},
		{"operation", operationScreens(s)},
		{"warning", problemScreens(s, "warning")},
		{"routine", routineScreens(s, now)},
	}
	for _, t := range tiers {
		if len(t.screens) == 0 {
			continue
		}
		sc := t.screens[int(now.Unix()/5)%len(t.screens)] // rotate every 5 s
		f := Frame{Line1: fit(sc[0]), Line2: fit(sc[1]), Tier: t.name, Backlight: true}
		if t.name == "critical" {
			f.Blink = true
			f.Backlight = now.Unix()%2 == 0 // blink the backlight to draw attention
		}
		if t.name == "routine" {
			f.Backlight = e.AlwaysOn() || now.Before(e.wakeUntil)
		}
		return f
	}
	return Frame{Line1: fit(""), Line2: fit("")}
}

func phaseScreens(p [2]string) []screen {
	if p[0] == "" {
		return nil
	}
	return []screen{{p[0], p[1]}}
}

var problemTitles = map[string]string{
	"raid_failed": "VOLUME FAILED", "raid_degraded": "VOLUME DEGRADED", "disk_failed": "DISK FAILING",
	"disk_warning": "DISK WARNING", "disk_hot": "DISK TOO HOT", "fan_failed": "FAN STOPPED",
	"system_hot": "SYSTEM TOO HOT", "volume_full": "VOLUME FULL", "ups_low_battery": "UPS BATTERY LOW",
	"ups_lost": "UPS NOT FOUND", "quota": "QUOTA REACHED", "scrub_mismatch": "CHECK: ISSUES",
}

func problemScreens(s Snapshot, severity string) []screen {
	var out []screen
	for _, p := range s.Problems {
		if p.Severity != severity {
			continue
		}
		title := problemTitles[p.Kind]
		if title == "" {
			title = strings.ToUpper(strings.ReplaceAll(p.Kind, "_", " "))
		}
		line2 := strings.TrimSpace(p.Where + " " + p.Detail)
		switch p.Kind {
		case "disk_failed":
			if p.Where != "" {
				line2 = "Replace " + strings.ToLower(p.Where[:1]) + p.Where[1:]
			}
		case "raid_degraded":
			for _, v := range s.Volumes {
				if v.Name == p.Where && v.SyncAction == "recover" {
					line2 = fmt.Sprintf("Rebuild %.0f%% %s", v.SyncPct, eta(v.ETA))
				}
			}
		}
		out = append(out, screen{title, line2})
	}
	return out
}

func networkScreens(s Snapshot, now time.Time) []screen {
	if s.NetPending.IsZero() {
		return nil
	}
	left := int(s.NetPending.Sub(now).Seconds())
	if left < 0 {
		left = 0
	}
	return []screen{{"Confirm network", fmt.Sprintf("rollback in %ds", left)}}
}

func upsScreens(s Snapshot) []screen {
	if !s.UPS.OnBattery {
		return nil
	}
	return []screen{{"ON UPS BATTERY", fmt.Sprintf("%d%%  %d min left", s.UPS.Charge, s.UPS.RuntimeMin)}}
}

func (e *Engine) noticeScreens(now time.Time) []screen {
	if now.Before(e.noticeUntil) {
		return []screen{e.notice}
	}
	return nil
}

func feedbackScreens(s Snapshot) []screen {
	var out []screen
	if s.Button.Running {
		out = append(out, screen{fmt.Sprintf("Backup %d/%d...", max(s.Button.Cur, 1), max(s.Button.Total, 1)), "Button pressed"})
	}
	for _, l := range s.Locating {
		out = append(out, screen{"Locate:", l + " blinking"})
	}
	return out
}

var syncNames = map[string]string{"resync": "Sync", "recover": "Rebuild", "check": "Check", "repair": "Repair", "reshape": "Expand"}

func operationScreens(s Snapshot) []screen {
	var out []screen
	for _, v := range s.Volumes {
		if v.SyncAction != "" {
			name := syncNames[v.SyncAction]
			if name == "" {
				name = v.SyncAction
			}
			out = append(out, screen{name + " " + v.Name, fmt.Sprintf("%.0f%%  %s", v.SyncPct, eta(v.ETA))})
		}
	}
	if s.Job != "" && len(out) == 0 {
		out = append(out, screen{"Working...", s.Job})
	}
	return out
}

func routineScreens(s Snapshot, now time.Time) []screen {
	ip := s.IP
	if ip == "" {
		ip = "no network"
	}
	out := []screen{{s.Hostname, ip}}
	for _, v := range s.Volumes {
		out = append(out, screen{fmt.Sprintf("%s %d%%", v.Name, v.UsedPct), size(v.FreeBytes) + " free"})
	}
	if s.CPUTemp > 0 || s.DiskTemp > 0 || s.FanRPM >= 0 {
		l1 := ""
		if s.CPUTemp > 0 {
			l1 += fmt.Sprintf("CPU %dC ", s.CPUTemp)
		}
		if s.DiskTemp > 0 {
			l1 += fmt.Sprintf("Disk %dC", s.DiskTemp)
		}
		l2 := ""
		if s.FanRPM >= 0 {
			l2 = fmt.Sprintf("Fan %d rpm", s.FanRPM)
		}
		out = append(out, screen{strings.TrimSpace(l1), l2})
	}
	out = append(out, screen{fmt.Sprintf("Up %d days", int(s.Uptime.Hours()/24)), now.Format("15:04  Mon 2 Jan")})
	if s.PowerOff != "" {
		out = append(out, screen{"Off at " + s.PowerOff, "On at " + s.PowerOn})
	}
	return out
}

func eta(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("~%dh%02d", h, m)
	}
	return fmt.Sprintf("~%dmin", max(m, 1))
}

func size(b int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f := float64(b)
	i := 0
	for f >= 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	if f < 10 && i > 0 {
		return fmt.Sprintf("%.1f %s", f, units[i])
	}
	return fmt.Sprintf("%.0f %s", f, units[i])
}

var translit = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e", "í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c", "ñ", "n",
	"À", "A", "É", "E", "È", "E", "Ê", "E", "Ç", "C", "°", "", "…", "...", "→", "->", "·", "-",
)

// fit makes a line displayable: plain ASCII (character LCDs have no
// accents), exactly Width characters.
func fit(s string) string {
	s = translit.Replace(s)
	var b strings.Builder
	for _, r := range s {
		if r < 32 || r > 126 {
			r = '?'
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > Width {
		out = out[:Width]
	}
	return out + strings.Repeat(" ", Width-len(out))
}
