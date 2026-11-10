package sysconf

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"readynas/internal/store"
)

const uuLog = `2026-11-09 06:19:47,561 INFO Starting unattended upgrades script
2026-11-09 06:19:47,600 INFO Allowed origins are: origin=Debian
2026-11-09 06:19:54,042 INFO No packages found that can be upgraded unattended and no pending auto-removals
2026-11-10 06:52:40,645 INFO Starting unattended upgrades script
2026-11-10 06:52:47,131 INFO Packages that will be upgraded: openssl libssl3t64
2026-11-10 06:53:20,001 INFO All upgrades installed
2026-11-11 06:50:00,000 INFO Starting unattended upgrades script
2026-11-11 06:50:05,000 INFO Packages that will be upgraded: samba
2026-11-11 06:50:30,000 ERROR Installing the upgrades failed!
`

func TestAutoUpdates(t *testing.T) {
	s, _ := newTest(t)
	dir := t.TempDir()
	s.AptConfDir, s.UnattendedLog, s.RebootRequiredFile = dir, filepath.Join(dir, "uu.log"), filepath.Join(dir, "reboot-required")
	var events []string
	s.Notify = func(kind, subject, body string) { events = append(events, kind+": "+subject+" | "+body) }

	if err := s.SaveAutoUpdates(store.UpdateSettings{AutoReboot: true, RebootHour: 4, RebootMinute: 30}); err != nil {
		t.Fatal(err)
	}
	periodic, _ := os.ReadFile(filepath.Join(dir, autoUpgradesFile))
	reboot, _ := os.ReadFile(filepath.Join(dir, nasdUnattended))
	if !strings.Contains(string(periodic), `APT::Periodic::Unattended-Upgrade "1";`) ||
		!strings.Contains(string(reboot), `Automatic-Reboot "true";`) || !strings.Contains(string(reboot), `Automatic-Reboot-Time "04:30";`) {
		t.Errorf("config:\n%s\n%s", periodic, reboot)
	}
	s.SaveAutoUpdates(store.UpdateSettings{AutoDisabled: true})
	if periodic, _ := os.ReadFile(filepath.Join(dir, autoUpgradesFile)); !strings.Contains(string(periodic), `Unattended-Upgrade "0";`) {
		t.Errorf("off:\n%s", periodic)
	}

	// The first look only takes note of the runs already in the log.
	os.WriteFile(s.UnattendedLog, []byte(strings.Join(strings.SplitAfter(uuLog, "\n")[:6], "")), 0o644)
	s.CheckAutoUpdates()
	if len(events) != 0 {
		t.Fatalf("old runs reported: %v", events)
	}
	// A new run that failed is reported once.
	os.WriteFile(s.UnattendedLog, []byte(uuLog), 0o644)
	s.CheckAutoUpdates()
	s.CheckAutoUpdates()
	if len(events) != 1 || !strings.HasPrefix(events[0], "updates_failed: ") || !strings.Contains(events[0], "samba") {
		t.Fatalf("events: %v", events)
	}
	last, runs := s.AutoRuns()
	if last.Day() != 11 || len(runs) != 2 || !runs[0].OK || runs[0].Packages[1] != "libssl3t64" || runs[1].OK || runs[1].Error == "" {
		t.Errorf("runs: %v %+v", last, runs)
	}

	if need, _ := s.RebootRequired(); need {
		t.Error("no restart needed yet")
	}
	os.WriteFile(s.RebootRequiredFile, nil, 0o644)
	os.WriteFile(s.RebootRequiredFile+".pkgs", []byte("linux-image-amd64\nlinux-image-amd64\n"), 0o644)
	if need, pkgs := s.RebootRequired(); !need || len(pkgs) != 1 {
		t.Errorf("restart: %v %v", need, pkgs)
	}
}
