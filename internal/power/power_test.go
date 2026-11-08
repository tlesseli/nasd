package power

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

type fake struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fake) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	f.mu.Unlock()
	switch name {
	case "lsblk":
		return []byte(`{"blockdevices":[
		 {"name":"sda","path":"/dev/sda","size":100,"type":"disk","tran":"sata","rota":true,"mountpoints":[null]},
		 {"name":"sdb","path":"/dev/sdb","size":100,"type":"disk","tran":"sata","rota":false,"mountpoints":[null]},
		 {"name":"sde","path":"/dev/sde","size":100,"type":"disk","tran":"usb","rota":true,"mountpoints":[null]}]}`), nil
	case "upsc":
		return []byte("battery.charge: 42\nbattery.runtime: 600\nups.load: 35\nups.model: Back-UPS\nups.status: OB DISCHRG\n"), nil
	}
	return nil, nil
}

func (f *fake) all() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.cmds, "\n") }

func newTest(t *testing.T) (*Service, *fake) {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"))
	f := &fake{}
	s := NewService(st, disks.NewService(f.run), jobs.NewManager(f.run))
	s.Run = f.run
	s.NutDir = t.TempDir()
	s.Sys = t.TempDir()
	s.settle = 0
	return s, f
}

func TestSpinDownValue(t *testing.T) {
	for min, want := range map[int]int{0: 0, 10: 120, 20: 240, 30: 241, 60: 242, 120: 244, 240: 248} {
		if got := spinDownValue(min); got != want {
			t.Errorf("spinDownValue(%d) = %d, want %d", min, got, want)
		}
	}
}

func TestApply(t *testing.T) {
	s, f := newTest(t)
	err := s.Save(context.Background(), store.PowerSettings{SpinDownMinutes: 30, UPS: store.UPS{Enabled: true, Driver: "dummy-ups"}})
	if err != nil {
		t.Fatal(err)
	}
	all := f.all()
	if !strings.Contains(all, "hdparm -S 241 /dev/sda") || strings.Contains(all, "hdparm -S 241 /dev/sdb") || strings.Contains(all, "hdparm -S 241 /dev/sde") {
		t.Errorf("spin-down only for rotating internal data disks:\n%s", all)
	}
	ups, _ := os.ReadFile(filepath.Join(s.NutDir, "ups.conf"))
	mon, _ := os.ReadFile(filepath.Join(s.NutDir, "upsmon.conf"))
	if !strings.Contains(string(ups), "driver = dummy-ups") || !strings.Contains(string(ups), "dummy.dev") ||
		!strings.Contains(string(mon), "MONITOR ups@localhost 1 nasd ") {
		t.Errorf("nut config:\n%s\n%s", ups, mon)
	}
	if fi, _ := os.Stat(filepath.Join(s.NutDir, "upsd.users")); fi.Mode().Perm()&0o004 != 0 {
		t.Error("upsd.users holds a password: it must not be world-readable")
	}
	if err := s.Save(context.Background(), store.PowerSettings{SpinDownMinutes: 7}); err == nil {
		t.Error("7 minutes is not an allowed choice")
	}
}

func TestUPSStatus(t *testing.T) {
	s, _ := newTest(t)
	s.Store.Update(func(st *store.State) error { st.Power.UPS = store.UPS{Enabled: true, Driver: "usbhid-ups"}; return nil })
	u := s.UPSStatus(context.Background())
	if !u.OnBattery || u.LowBattery || u.Charge != 42 || u.RuntimeSec != 600 || u.Load != 35 || u.Model != "Back-UPS" {
		t.Errorf("%+v", u)
	}
}

func TestNextWake(t *testing.T) {
	timer := store.PowerTimer{OnHour: 7, OnMinute: 30}
	night := time.Date(2026, 11, 9, 23, 0, 0, 0, time.UTC)
	if w := NextWake(timer, night); !w.Equal(time.Date(2026, 11, 10, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("off at 23:00 -> on next morning, got %s", w)
	}
	morning := time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)
	if w := NextWake(timer, morning); !w.Equal(time.Date(2026, 11, 9, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("off at 06:00 -> on the same day, got %s", w)
	}
}

func TestTimerWaitsForJobs(t *testing.T) {
	s, f := newTest(t)
	now := time.Date(2026, 11, 9, 23, 0, 5, 0, time.UTC)
	s.Now = func() time.Time { return now }
	s.Store.Update(func(st *store.State) error {
		st.Power.Timer = store.PowerTimer{Off: store.Schedule{Kind: "daily", Hour: 23}, OnHour: 7}
		return nil
	})
	release := make(chan struct{})
	s.Jobs.Start("rebuild", time.Minute, func(context.Context, *jobs.Job) error { <-release; return nil })
	s.Tick(context.Background())
	if strings.Contains(f.all(), "poweroff") {
		t.Fatal("must not power off while a job runs")
	}
	close(release)
	s.Jobs.Wait()
	now = now.Add(time.Minute)
	s.Tick(context.Background())
	all := f.all()
	wake := time.Date(2026, 11, 10, 7, 0, 0, 0, time.UTC).Unix()
	if !strings.Contains(all, "systemctl poweroff") {
		t.Fatalf("expected rtcwake then poweroff:\n%s", all)
	}
	if !strings.Contains(all, "rtcwake -m no -t "+itoa(wake)) {
		t.Errorf("wake alarm should be %d:\n%s", wake, all)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
