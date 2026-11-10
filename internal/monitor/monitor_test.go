package monitor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/disks"
	"readynas/internal/store"
	"readynas/internal/volumes"
)

type env struct {
	m      *Monitor
	sys    string
	events func() []string
}

func setup(t *testing.T) env {
	sys := t.TempDir()
	md := filepath.Join(sys, "block", "md127", "md")
	os.MkdirAll(md, 0o755)
	write := func(f, v string) { os.WriteFile(filepath.Join(md, f), []byte(v+"\n"), 0o644) }
	for f, v := range map[string]string{"array_state": "clean", "level": "raid5", "raid_disks": "3", "degraded": "0",
		"sync_action": "idle", "last_sync_action": "idle", "mismatch_cnt": "0"} {
		write(f, v)
	}
	fake := func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "mdadm":
			return []byte("MD_DEVNAME=media\n"), nil
		case "lsblk":
			if len(args) > 0 && args[len(args)-1] == "/dev/md127" {
				return []byte(`{"blockdevices":[{"fstype":"ext4","uuid":"u","mountpoints":[null]}]}`), nil
			}
			return []byte(`{"blockdevices":[{"name":"sda","path":"/dev/sda","size":100,"model":"M","serial":"S1","type":"disk","tran":"sata","mountpoints":[null]}]}`), nil
		case "smartctl":
			return []byte(os.Getenv("FAKE_SMART")), nil
		}
		return nil, nil
	}
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"))
	vs := &volumes.Service{Sys: sys, Run: fake, SkipStatfs: true}
	ds := disks.NewService(fake)
	a := alerts.NewService(st)
	m := New(st, vs, ds, a)
	m.Sys = sys
	return env{m: m, sys: sys, events: func() []string {
		var out []string
		s := st.Read()
		for _, e := range s.Events {
			out = append(out, e.Kind+": "+e.Subject)
		}
		return out
	}}
}

func (e env) set(f, v string) {
	os.WriteFile(filepath.Join(e.sys, "block", "md127", "md", f), []byte(v+"\n"), 0o644)
}

func TestDegradedAlertOnceThenResolved(t *testing.T) {
	t.Setenv("FAKE_SMART", `{"smart_status":{"passed":true},"temperature":{"current":35}}`)
	e := setup(t)
	ctx := context.Background()
	e.m.Tick(ctx)
	if len(e.events()) != 0 {
		t.Fatalf("healthy system, no events expected: %v", e.events())
	}
	e.set("degraded", "1")
	e.m.Tick(ctx)
	e.m.Tick(ctx)
	if ev := e.events(); len(ev) != 1 || !strings.HasPrefix(ev[0], "raid_degraded: Volume media is degraded") {
		t.Fatalf("one degraded alert expected: %v", ev)
	}
	e.set("degraded", "0")
	e.m.Tick(ctx)
	if ev := e.events(); len(ev) != 2 || !strings.HasPrefix(ev[1], "resolved:") {
		t.Fatalf("resolved expected: %v", ev)
	}
}

func TestNewRaid5BuildIsNotAnAlert(t *testing.T) {
	t.Setenv("FAKE_SMART", `{"smart_status":{"passed":true}}`)
	e := setup(t)
	md := filepath.Join(e.sys, "block", "md127", "md", "dev-sdc1")
	os.MkdirAll(md, 0o755)
	os.WriteFile(filepath.Join(md, "state"), []byte("spare\n"), 0o644)
	os.WriteFile(filepath.Join(md, "slot"), []byte("2\n"), 0o644)
	e.set("degraded", "1")
	e.set("sync_action", "recover")
	e.m.Tick(context.Background())
	if ev := e.events(); len(ev) != 0 {
		t.Fatalf("mdadm builds RAID 5 by rebuilding onto the last disk: no alert expected, got %v", ev)
	}
}

func TestCheckWithMismatchStartsRepair(t *testing.T) {
	t.Setenv("FAKE_SMART", `{"smart_status":{"passed":true}}`)
	e := setup(t)
	ctx := context.Background()
	e.m.Store.Update(func(s *store.State) error {
		s.Maintenance = map[string]store.Maintenance{"media": {AutoFix: true}}
		return nil
	})
	e.set("sync_action", "check")
	e.m.Tick(ctx)
	e.set("sync_action", "idle")
	e.set("last_sync_action", "check")
	e.set("mismatch_cnt", "128")
	e.m.Tick(ctx)
	time.Sleep(20 * time.Millisecond)
	if ev := e.events(); len(ev) != 1 || !strings.Contains(ev[0], "128 inconsistencies") {
		t.Fatalf("events: %v", ev)
	}
	if b, _ := os.ReadFile(filepath.Join(e.sys, "block", "md127", "md", "sync_action")); strings.TrimSpace(string(b)) != "repair" {
		t.Errorf("auto-fix must start a repair, sync_action = %q", b)
	}
}

func TestScheduledCheckAndDiskAlerts(t *testing.T) {
	t.Setenv("FAKE_SMART", `{"smart_status":{"passed":false},"temperature":{"current":61},
	  "ata_smart_attributes":{"table":[{"id":5,"raw":{"value":8}}]}}`)
	e := setup(t)
	e.m.Now = func() time.Time { return time.Date(2026, 11, 9, 2, 30, 0, 0, time.Local) }
	e.m.Store.Update(func(s *store.State) error {
		s.Maintenance = map[string]store.Maintenance{"media": {Scrub: store.Schedule{Kind: "daily", Hour: 2, Minute: 30}}}
		return nil
	})
	e.m.Tick(context.Background())
	if b, _ := os.ReadFile(filepath.Join(e.sys, "block", "md127", "md", "sync_action")); strings.TrimSpace(string(b)) != "check" {
		t.Errorf("scheduled check not started: %q", b)
	}
	got := strings.Join(e.events(), "\n")
	for _, want := range []string{"disk_failed: Disk sda is failing", "disk_warning: Disk sda has 8 bad sectors", "disk_hot: Disk sda is at 61 °C"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestSurfaceTestReportedOnce(t *testing.T) {
	e := setup(t)
	var problems []string
	cond := func(key, kind, subject, body string) { problems = append(problems, kind) }
	disk := func(serial string, bay int, st *disks.SelfTest) disks.Disk {
		return disks.Disk{Name: "sd" + string(rune('a'+bay-1)), Serial: serial, Bay: bay, Health: &disks.Health{Supported: true, Passed: true, SelfTest: st}}
	}
	check := func(list ...disks.Disk) {
		problems = nil
		e.m.checkSelfTests(list, e.m.Store.Read(), cond)
	}

	// First look: an old test of disk 1 and a never-tested disk 2 are not news.
	check(disk("S1", 1, &disks.SelfTest{LastLong: &disks.TestResult{Passed: true, Hours: 100}}),
		disk("S2", 2, &disks.SelfTest{}))
	if ev := e.events(); len(ev) != 0 {
		t.Fatalf("nothing new: %v", ev)
	}
	// Disk 1 finished a new test, disk 2 is still testing: wait for it.
	check(disk("S1", 1, &disks.SelfTest{LastLong: &disks.TestResult{Passed: true, Hours: 200}}),
		disk("S2", 2, &disks.SelfTest{Running: true}))
	if ev := e.events(); len(ev) != 0 {
		t.Fatalf("disk 2 still testing: %v", ev)
	}
	// Disk 2 finished: one event for both.
	final := []disks.Disk{disk("S1", 1, &disks.SelfTest{LastLong: &disks.TestResult{Passed: true, Hours: 200}}),
		disk("S2", 2, &disks.SelfTest{LastLong: &disks.TestResult{Passed: true, Hours: 50}})}
	check(final...)
	check(final...)
	if ev := e.events(); len(ev) != 1 || ev[0] != "disk_test_done: Surface test passed: Bay 1 (sda), Bay 2 (sdb)" {
		t.Fatalf("one event expected: %v", ev)
	}
	// A failed test is a problem for as long as it is the newest.
	check(disk("S1", 1, &disks.SelfTest{LastLong: &disks.TestResult{Failed: true, Status: "Completed: read failure", Hours: 300}}))
	if len(problems) != 1 || problems[0] != "disk_test_failed" {
		t.Fatalf("problems: %v", problems)
	}
}
