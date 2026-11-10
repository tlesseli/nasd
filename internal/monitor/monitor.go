// Package monitor watches the NAS and raises alerts.
//
// Every 30 s it evaluates a list of "conditions" (volume degraded, disk too
// hot...). Each condition has a key, e.g. "raid_degraded:media". An alert is
// sent when a key appears, not while it stays true: you get one email when
// a disk fails, not one every 30 seconds. When it disappears, a "resolved"
// event says so.
//
// It also starts scheduled volume checks and reacts when they finish.
package monitor

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/disks"
	"readynas/internal/health"
	"readynas/internal/power"
	"readynas/internal/store"
	"readynas/internal/sysconf"
	"readynas/internal/volumes"
)

type Monitor struct {
	Store   *store.Store
	Volumes *volumes.Service
	Disks   *disks.Service
	Alerts  *alerts.Service
	Power   *power.Service   // optional
	Updates *sysconf.Service // optional: automatic updates, restart needed
	Sys     string           // "/sys"
	Now     func() time.Time

	mu         sync.Mutex         // Tick (monitor goroutine) vs MarkStarted (HTTP handlers)
	active     map[string]Problem // condition key -> current problem
	lastAction map[string]string  // volume -> sync action at the previous tick
	lastFired  map[string]time.Time
	fanSeen    map[string]bool // fans seen spinning (a fan at 0 RPM from the start may just be unplugged)
	upsFails   int             // consecutive rounds without an answer from the UPS
	// testsPassed: disks whose surface test passed, told in one event once
	// no disk is testing any more.
	testsPassed []string
}

func New(st *store.Store, v *volumes.Service, d *disks.Service, a *alerts.Service) *Monitor {
	return &Monitor{Store: st, Volumes: v, Disks: d, Alerts: a, Sys: "/sys", Now: time.Now,
		active: map[string]Problem{}, lastAction: map[string]string{}, lastFired: map[string]time.Time{}, fanSeen: map[string]bool{}}
}

func (m *Monitor) Run(ctx context.Context) {
	m.Tick(ctx)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Tick(ctx)
		}
	}
}

// Problem is a condition that is true right now (shown as "Needs attention").
type Problem struct {
	Key      string    `json:"key"` // e.g. "raid_degraded:media"
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Subject  string    `json:"subject"`
	Body     string    `json:"body"`
	Since    time.Time `json:"since"`
}

// Active lists current problems, most severe first.
func (m *Monitor) Active() []Problem {
	m.mu.Lock()
	defer m.mu.Unlock()
	rank := map[string]int{"critical": 0, "warning": 1, "info": 2}
	out := make([]Problem, 0, len(m.active))
	for _, p := range m.active {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}

// MarkStarted records that a check/repair was started by hand, so its end
// is reported even if it completes between two ticks.
func (m *Monitor) MarkStarted(volume, action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAction[volume] = action
}

// Tick runs one round of checks (exported for tests).
func (m *Monitor) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.Store.Read()
	seen := map[string]bool{}
	cond := func(key, kind, subject, body string) {
		seen[key] = true
		if _, ok := m.active[key]; !ok {
			m.active[key] = Problem{Key: key, Kind: kind, Severity: alerts.Severity(kind), Subject: subject, Body: body, Since: m.Now()}
			m.Alerts.Notify(kind, subject, body)
		}
	}

	tempLimit := orDefault(st.Alerts.DiskTempLimit, alerts.DefaultDiskTemp)
	fullPct := orDefault(st.Alerts.VolumeFullPercent, alerts.DefaultFullPct)

	vols, err := m.Volumes.List(ctx)
	if err != nil {
		log.Printf("monitor: %v", err)
	}
	for _, v := range vols {
		m.checkVolume(ctx, v, st, cond, fullPct)
	}

	list, err := m.Disks.List(ctx)
	if err != nil {
		log.Printf("monitor: %v", err)
	}
	m.checkSelfTests(list, st, cond)
	for _, d := range list {
		h := d.Health
		if h == nil || !h.Supported {
			continue
		}
		name := fmt.Sprintf("%s (%s %s)", d.Name, d.Model, d.Serial)
		if !h.Passed {
			cond("disk_failed:"+d.Serial, "disk_failed", "Disk "+d.Name+" is failing",
				"SMART reports that disk "+name+" is failing. Back up your data and replace it as soon as possible.")
		}
		if bad := h.Reallocated + h.Pending + h.Uncorrectable; bad > 0 {
			// The count is in the key: more bad sectors later = a new alert.
			cond(fmt.Sprintf("disk_warning:%s:%d", d.Serial, bad), "disk_warning",
				fmt.Sprintf("Disk %s has %d bad sectors", d.Name, bad),
				fmt.Sprintf("Disk %s: %d reallocated, %d pending, %d uncorrectable sectors. A growing number means the disk is wearing out.",
					name, h.Reallocated, h.Pending, h.Uncorrectable))
		}
		if h.TemperatureC >= tempLimit {
			cond("disk_hot:"+d.Serial, "disk_hot", fmt.Sprintf("Disk %s is at %d °C", d.Name, h.TemperatureC),
				fmt.Sprintf("Disk %s is above the %d °C limit. Check the fan and the room temperature.", name, tempLimit))
		}
	}

	for _, s := range health.Sensors(m.Sys) {
		switch s.Kind {
		case "fan":
			if s.Value > 0 {
				m.fanSeen[s.ID] = true
			} else if m.fanSeen[s.ID] {
				cond("fan_failed:"+s.ID, "fan_failed", "Fan "+s.Label+" stopped",
					"The fan "+s.Label+" ("+s.Chip+") reports 0 RPM. Without cooling, disks overheat: check it soon.")
			}
		case "temp":
			if s.Limit > 0 && s.Value >= s.Limit {
				cond("system_hot:"+s.ID, "system_hot", fmt.Sprintf("%s temperature is %.0f °C", s.Label, s.Value),
					fmt.Sprintf("Sensor %s (%s) reached its %.0f °C limit.", s.Label, s.Chip, s.Limit))
			}
		}
	}

	if m.Power != nil {
		m.checkUPS(ctx, cond)
	}
	if m.Updates != nil {
		m.checkUpdates(st, cond)
	}

	// Conditions that are no longer true.
	for key, p := range m.active {
		if !seen[key] {
			delete(m.active, key)
			if !strings.HasPrefix(key, "disk_warning:") { // bad sectors don't "heal"
				m.Alerts.Notify("resolved", "Resolved: "+p.Subject, "This problem is no longer detected.")
			}
		}
	}

	m.runSchedules(ctx, st, vols)
	m.runDiskTests(ctx, st, vols, list)
}

// diskLabel: "Bay 2 (sdb)", or the device name for a disk outside the bays.
func diskLabel(d disks.Disk) string {
	if d.Bay > 0 {
		return fmt.Sprintf("Bay %d (%s)", d.Bay, d.Name)
	}
	return d.Name
}

// checkSelfTests reports the result of each disk's newest extended SMART
// test once, whoever started it. A failed one stays a problem until a later
// test passes; passed ones are told together when every test has ended.
func (m *Monitor) checkSelfTests(list []disks.Disk, st store.State, cond func(string, string, string, string)) {
	seen := map[string]int{}
	running := false
	for _, d := range list {
		h := d.Health
		if h == nil || h.SelfTest == nil || d.Serial == "" {
			continue
		}
		t := h.SelfTest
		running = running || t.Running
		last, known := st.DiskTests.Reported[d.Serial]
		switch {
		case t.LastLong == nil:
			if !known {
				seen[d.Serial] = -1 // never tested: its first test will be new
			}
		case !known:
			seen[d.Serial] = t.LastLong.Hours // from before nasd watched: not news
		case t.LastLong.Hours > last:
			seen[d.Serial] = t.LastLong.Hours
			if t.LastLong.Passed {
				m.testsPassed = append(m.testsPassed, diskLabel(d))
			}
		}
		if t.LastLong != nil && t.LastLong.Failed {
			cond("disk_test_failed:"+d.Serial, "disk_test_failed", "Disk "+diskLabel(d)+" failed its surface test",
				fmt.Sprintf("The SMART extended test of disk %s (%s %s) ended with: %s. The disk can't read part of its surface: back up your data and replace it.",
					diskLabel(d), d.Model, d.Serial, t.LastLong.Status))
		}
	}
	if len(seen) > 0 {
		m.Store.Update(func(st *store.State) error {
			if st.DiskTests.Reported == nil {
				st.DiskTests.Reported = map[string]int{}
			}
			for serial, hours := range seen {
				st.DiskTests.Reported[serial] = hours
			}
			return nil
		})
	}
	if !running && len(m.testsPassed) > 0 {
		sort.Strings(m.testsPassed)
		m.Alerts.Notify("disk_test_done", "Surface test passed: "+strings.Join(m.testsPassed, ", "),
			"The SMART extended test read the whole surface of these disks without finding a problem.")
		m.testsPassed = nil
	}
}

// runDiskTests starts the scheduled surface tests: every internal disk
// tests itself at once (they run inside the disks, in parallel). Not while
// a volume rebuilds or reshapes: that needs every disk at full speed.
func (m *Monitor) runDiskTests(ctx context.Context, st store.State, vols []volumes.Volume, list []disks.Disk) {
	now := m.Now().Truncate(time.Minute)
	if !st.DiskTests.Schedule.Due(now) || m.lastFired["disktests"].Equal(now) {
		return
	}
	m.lastFired["disktests"] = now
	for _, v := range vols {
		if v.Status == "rebuilding" || v.Status == "reshaping" {
			log.Printf("scheduled disk tests skipped: volume %s is %s", v.Name, v.Status)
			return
		}
	}
	for _, d := range list {
		if d.Role == "usb" || d.Health == nil || !d.Health.Supported || (d.Health.SelfTest != nil && d.Health.SelfTest.Running) {
			continue
		}
		if err := m.Disks.SelfTest(ctx, d.Name, "long"); err != nil {
			log.Printf("scheduled surface test of %s not started: %v", d.Name, err)
			continue
		}
		log.Printf("scheduled surface test of %s started", d.Name)
	}
}

func (m *Monitor) checkVolume(ctx context.Context, v volumes.Volume, st store.State, cond func(string, string, string, string), fullPct int) {
	switch {
	case v.Status == "failed":
		cond("raid_failed:"+v.Name, "raid_failed", "Volume "+v.Name+" has failed",
			fmt.Sprintf("Volume %s (%s) lost more disks than it can survive. Its data is not accessible.", v.Name, v.Level))
	case missingDisks(v) > 0 || hasFaulty(v):
		cond("raid_degraded:"+v.Name, "raid_degraded", "Volume "+v.Name+" is degraded",
			fmt.Sprintf("Volume %s (%s) lost a disk and has no redundancy left: one more failure loses data. Put the disk back or replace it in the Volumes page.", v.Name, v.Level))
	}
	if v.TotalBytes > 0 && v.UsedBytes*100 >= v.TotalBytes*int64(fullPct) {
		cond("volume_full:"+v.Name, "volume_full", fmt.Sprintf("Volume %s is %d%% full", v.Name, v.UsedBytes*100/v.TotalBytes),
			fmt.Sprintf("Volume %s has %d MB free.", v.Name, v.AvailBytes>>20))
	}

	if v.Quotas {
		m.checkQuotas(ctx, v, st, cond)
	}

	// React to the end of a sync operation (compare with the last tick).
	prev, had := m.lastAction[v.Name]
	cur := "idle"
	if v.Sync != nil {
		cur = v.Sync.Action
	}
	m.lastAction[v.Name] = cur
	if !had || prev == cur || cur != "idle" {
		return
	}
	switch prev {
	case "recover":
		// If a "degraded" alert is pending, its "Resolved" message says it
		// already: one email per piece of news. This one then mostly reports
		// the end of a new RAID 5's initial build.
		if _, alerted := m.active["raid_degraded:"+v.Name]; v.Degraded == 0 && !alerted {
			m.Alerts.Notify("raid_rebuilt", "Volume "+v.Name+" is fully protected again", "The rebuild finished: the volume has its redundancy back.")
		}
	case "check", "repair":
		m.Store.Update(func(st *store.State) error {
			if st.Maintenance == nil {
				st.Maintenance = map[string]store.Maintenance{}
			}
			mt := st.Maintenance[v.Name]
			mt.LastCheck, mt.LastMismatch = m.Now(), v.MismatchCount
			st.Maintenance[v.Name] = mt
			return nil
		})
	}
	switch prev {
	case "check":
		if v.MismatchCount == 0 {
			m.Alerts.Notify("scrub_done", "Volume "+v.Name+" checked: no problem found", "Every block matched its mirror or parity.")
			return
		}
		body := fmt.Sprintf("The check of volume %s found %d sectors that didn't match their parity or mirror.", v.Name, v.MismatchCount)
		if st.Maintenance[v.Name].AutoFix {
			if err := m.Volumes.StartCheck(v, "repair"); err == nil {
				body += " A repair has started automatically."
			}
		} else {
			body += " Run a repair from the Volumes page (or enable automatic repair)."
		}
		m.Alerts.Notify("scrub_mismatch", fmt.Sprintf("Volume %s: %d inconsistencies found", v.Name, v.MismatchCount), body)
	case "repair":
		m.Alerts.Notify("scrub_done", "Volume "+v.Name+" repaired", fmt.Sprintf("The repair rewrote %d sectors.", v.MismatchCount))
	}
}

// runSchedules starts scheduled volume checks.
func (m *Monitor) runSchedules(ctx context.Context, st store.State, vols []volumes.Volume) {
	now := m.Now().Truncate(time.Minute)
	for _, v := range vols {
		mt, ok := st.Maintenance[v.Name]
		if !ok || !mt.Scrub.Due(now) || m.lastFired[v.Name].Equal(now) {
			continue
		}
		m.lastFired[v.Name] = now
		if err := m.Volumes.StartCheck(v, "check"); err != nil {
			log.Printf("scheduled check of %s not started: %v", v.Name, err)
			continue
		}
		log.Printf("scheduled check of %s started", v.Name)
		m.lastAction[v.Name] = "check" // so the end is noticed even if the check is very short
	}
}

func (m *Monitor) checkQuotas(ctx context.Context, v volumes.Volume, st store.State, cond func(string, string, string, string)) {
	usage, err := m.Volumes.QuotaUsage(ctx, v)
	if err != nil {
		return
	}
	for _, q := range usage {
		if u := st.FindUser(q.User); q.LimitKB == 0 || u == nil {
			continue
		}
		pct := q.UsedKB * 100 / q.LimitKB
		switch {
		case q.UsedKB >= q.LimitKB:
			cond("quota_full:"+v.Name+":"+q.User, "quota", fmt.Sprintf("%s reached their quota on %s", q.User, v.Name),
				fmt.Sprintf("%s uses %d MB of their %d MB on volume %s and can't save more files there.", q.User, q.UsedKB>>10, q.LimitKB>>10, v.Name))
		case pct >= 90:
			cond("quota_near:"+v.Name+":"+q.User, "quota", fmt.Sprintf("%s is at %d%% of their quota on %s", q.User, pct, v.Name),
				fmt.Sprintf("%s uses %d MB of their %d MB on volume %s.", q.User, q.UsedKB>>10, q.LimitKB>>10, v.Name))
		}
	}
}

func (m *Monitor) checkUPS(ctx context.Context, cond func(string, string, string, string)) {
	u := m.Power.UPSStatus(ctx)
	if !u.Enabled {
		return
	}
	if u.Error != "" {
		// The driver needs a few seconds after (re)starting: only complain
		// after two silent rounds in a row.
		if m.upsFails++; m.upsFails >= 2 {
			cond("ups_lost", "ups_lost", "The UPS doesn't answer", u.Error+". Check its USB cable.")
		}
		return
	}
	m.upsFails = 0
	if u.LowBattery {
		cond("ups_low", "ups_low_battery", "UPS battery low: shutting down",
			fmt.Sprintf("The battery is at %d%%. The NAS shuts down cleanly now to protect your data.", u.Charge))
	} else if u.OnBattery {
		cond("ups_on_battery", "ups_on_battery", "Power cut: the NAS runs on its UPS",
			fmt.Sprintf("Battery at %d%%, about %d minutes left. The NAS will shut down by itself if the battery runs low.", u.Charge, u.RuntimeSec/60))
	}
}

// checkUpdates reports what the automatic updates installed, and keeps a
// "restart needed" problem while an update waits for one.
func (m *Monitor) checkUpdates(st store.State, cond func(string, string, string, string)) {
	m.Updates.CheckAutoUpdates()
	need, pkgs := m.Updates.RebootRequired()
	if !need {
		return
	}
	body := "An installed update takes effect after a restart"
	if len(pkgs) > 0 {
		body += " (" + strings.Join(pkgs, ", ") + ")"
	}
	if u := st.Updates; u.AutoReboot && !u.AutoDisabled {
		body += fmt.Sprintf(". The NAS restarts by itself at %02d:%02d.", u.RebootHour, u.RebootMinute)
	} else {
		body += ". Restart the NAS when convenient (System > Restart)."
	}
	cond("reboot_required", "reboot_required", "A restart is needed to finish an update", body)
}

func missingDisks(v volumes.Volume) int {
	rebuilding := 0
	for _, mem := range v.Members {
		if strings.Contains(mem.State, "spare") && mem.Slot >= 0 {
			rebuilding++ // replacing a missing disk (also how mdadm builds a new RAID 5)
		}
	}
	return v.Degraded - rebuilding
}

func hasFaulty(v volumes.Volume) bool {
	for _, mem := range v.Members {
		if strings.Contains(mem.State, "faulty") {
			return true
		}
	}
	return false
}

func orDefault(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
