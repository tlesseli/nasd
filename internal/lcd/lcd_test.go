package lcd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 8, 21, 14, 0, 0, time.UTC) // even second, rotation slot 0

func engine(s Snapshot) *Engine {
	e := New(nil, nil)
	e.Now = func() time.Time { return t0 }
	e.snap = s
	return e
}

func routine() Snapshot {
	return Snapshot{Hostname: "nas-01", IP: "192.168.1.50", FanRPM: -1, Uptime: 41 * 24 * time.Hour,
		Volumes: []Volume{{Name: "data", UsedPct: 57, FreeBytes: 2_100_000_000_000}}}
}

func TestFit(t *testing.T) {
	if got := fit("Sauvegardé → USB à 42°"); got != "Sauvegarde -> US" || len(got) != Width {
		t.Errorf("%q", got)
	}
	if got := fit("ab"); got != "       ab       " { // centered
		t.Errorf("%q", got)
	}
}

func TestPrioritiesAndBacklight(t *testing.T) {
	s := routine()
	e := engine(s)
	f := e.compute(t0)
	if f.Tier != "routine" || strings.TrimSpace(f.Line1) != "nas-01" || strings.TrimSpace(f.Line2) != "192.168.1.50" || f.Backlight {
		t.Fatalf("routine, backlight off: %+v", f)
	}
	e.wakeUntil = t0.Add(time.Minute)
	if !e.compute(t0).Backlight {
		t.Error("woken: backlight on")
	}

	// A rebuild beats routine; a critical problem beats the rebuild and blinks.
	s.Volumes[0].SyncAction, s.Volumes[0].SyncPct, s.Volumes[0].ETA = "recover", 42, 3*time.Hour+10*time.Minute
	e.snap = s
	if f := e.compute(t0); f.Tier != "operation" || f.Line1 != fit("Rebuild data") || f.Line2 != fit("42%  ~3h10") {
		t.Fatalf("operation: %+v", f)
	}
	s.Problems = []Problem{{Severity: "critical", Kind: "raid_degraded", Where: "data"}, {Severity: "critical", Kind: "disk_failed", Where: "Bay 3"}}
	e.snap = s
	f = e.compute(t0)
	if f.Tier != "critical" || f.Line1 != fit("VOLUME DEGRADED") || f.Line2 != fit("Rebuild 42% ~3h10") || !f.Blink || !f.Backlight {
		t.Fatalf("critical: %+v", f)
	}
	if f := e.compute(t0.Add(5 * time.Second)); f.Line1 != fit("DISK FAILING") || f.Line2 != fit("Replace bay 3") {
		t.Errorf("second critical screen after 5 s: %+v", f)
	}
	if f := e.compute(t0.Add(time.Second)); f.Backlight {
		t.Error("critical blinks: backlight off on odd seconds")
	}

	// Startup message beats everything.
	e.phase = [2]string{"Starting...", "Volumes"}
	if f := e.compute(t0); f.Tier != "phase" || f.Line1 != fit("Starting...") {
		t.Errorf("phase: %+v", f)
	}
}

func TestOtherTiers(t *testing.T) {
	s := routine()
	s.NetPending = t0.Add(75 * time.Second)
	s.UPS = UPS{OnBattery: true, Charge: 64, RuntimeMin: 12}
	s.Locating = []string{"Bay 3"}
	e := engine(s)
	if f := e.compute(t0); f.Line2 != fit("rollback in 75s") {
		t.Errorf("network first: %+v", f)
	}
	s.NetPending = time.Time{}
	e.snap = s
	if f := e.compute(t0); f.Line1 != fit("ON UPS BATTERY") || f.Line2 != fit("64%  12 min left") {
		t.Errorf("ups: %+v", f)
	}
	s.UPS = UPS{}
	e.snap = s
	if f := e.compute(t0); f.Line2 != fit("Bay 3 blinking") {
		t.Errorf("locate: %+v", f)
	}
}

func TestCharLCDOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lcd")
	os.WriteFile(path, nil, 0o644)
	c := &CharLCD{Path: path}
	c.Show(fit("nas-01"), fit("192.168.1.50"), false)
	got, _ := os.ReadFile(path)
	if string(got) != "\x1b[Lx0y0;     nas-01     \x1b[Lx0y1;  192.168.1.50  \x1b[L-" {
		t.Errorf("%q", got)
	}
}
