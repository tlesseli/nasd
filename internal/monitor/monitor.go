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
	"strings"
	"sync"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/disks"
	"readynas/internal/health"
	"readynas/internal/power"
	"readynas/internal/store"
	"readynas/internal/volumes"
)

type Monitor struct {
	Store   *store.Store
	Volumes *volumes.Service
	Disks   *disks.Service
	Alerts  *alerts.Service
	Power   *power.Service // optional
	Sys     string         // "/sys"
	Now     func() time.Time

	mu         sync.Mutex        // Tick (monitor goroutine) vs MarkStarted (HTTP handlers)
	active     map[string]string // condition key -> subject, for "resolved" messages
	lastAction map[string]string // volume -> sync action at the previous tick
	lastFired  map[string]time.Time
	fanSeen    map[string]bool // fans seen spinning (a fan at 0 RPM from the start may just be unplugged)
	upsFails   int             // consecutive rounds without an answer from the UPS
}

func New(st *store.Store, v *volumes.Service, d *disks.Service, a *alerts.Service) *Monitor {
	return &Monitor{Store: st, Volumes: v, Disks: d, Alerts: a, Sys: "/sys", Now: time.Now,
		active: map[string]string{}, lastAction: map[string]string{}, lastFired: map[string]time.Time{}, fanSeen: map[string]bool{}}
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
			m.active[key] = subject
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

	// Conditions that are no longer true.
	for key, subject := range m.active {
		if !seen[key] {
			delete(m.active, key)
			if !strings.HasPrefix(key, "disk_warning:") { // bad sectors don't "heal"
				m.Alerts.Notify("resolved", "Resolved: "+subject, "This problem is no longer detected.")
			}
		}
	}

	m.runSchedules(ctx, st, vols)
}

func (m *Monitor) checkVolume(ctx context.Context, v volumes.Volume, st store.State, cond func(string, string, string, string), fullPct int) {
	switch {
	case v.Status == "failed":
		cond("raid_failed:"+v.Name, "raid_failed", "Volume "+v.Name+" has failed",
			fmt.Sprintf("Volume %s (%s) lost more disks than it can survive. Its data is not accessible.", v.Name, v.Level))
	case missingDisks(v) > 0 || hasFaulty(v):
		cond("raid_degraded:"+v.Name, "raid_degraded", "Volume "+v.Name+" is degraded",
			fmt.Sprintf("Volume %s (%s) lost a disk and has no redundancy left: one more failure loses data. Replace the disk in the Volumes page.", v.Name, v.Level))
	}
	if v.TotalBytes > 0 && v.UsedBytes*100 >= v.TotalBytes*int64(fullPct) {
		cond("volume_full:"+v.Name, "volume_full", fmt.Sprintf("Volume %s is %d%% full", v.Name, v.UsedBytes*100/v.TotalBytes),
			fmt.Sprintf("Volume %s has %d MB free.", v.Name, v.AvailBytes>>20))
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
