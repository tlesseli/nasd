package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"readynas/internal/disks"
	"readynas/internal/hardware"
	"readynas/internal/health"
	"readynas/internal/jobs"
	"readynas/internal/lcd"
	"readynas/internal/monitor"
	"readynas/internal/power"
	"readynas/internal/store"
	"readynas/internal/system"
	"readynas/internal/volumes"
)

// lcdCollector gathers what the front display may show. It runs every 5 s,
// so it only reads cached or cheap information.
func lcdCollector(st *store.Store, vols *volumes.Service, dk *disks.Service, mon *monitor.Monitor,
	pw *power.Service, btn *hardware.Button, loc *hardware.Locator, jm *jobs.Manager, hwmon string) func(context.Context) lcd.Snapshot {
	return func(ctx context.Context) lcd.Snapshot {
		state := st.Read()
		s := lcd.Snapshot{FanRPM: -1}
		s.Hostname, _ = os.Hostname()
		s.IP = primaryIPv4()
		s.Uptime = time.Duration(system.Read().UptimeSeconds) * time.Second

		diskList, _ := dk.List(ctx)
		bayOf := map[string]string{} // disk name or serial -> "Bay N"
		for _, d := range diskList {
			label := d.Name
			if d.Bay > 0 {
				label = "Bay " + strconv.Itoa(d.Bay)
			}
			bayOf[d.Name], bayOf[d.Serial] = label, label
			if d.Role == "data" && d.Health != nil && d.Health.TemperatureC > s.DiskTemp {
				s.DiskTemp = d.Health.TemperatureC
			}
		}

		volList, _ := vols.List(ctx)
		for _, v := range volList {
			lv := lcd.Volume{Name: v.Name, FreeBytes: v.AvailBytes}
			if v.TotalBytes > 0 {
				lv.UsedPct = int(v.UsedBytes * 100 / v.TotalBytes)
			}
			if v.Sync != nil {
				lv.SyncAction, lv.SyncPct, lv.ETA = v.Sync.Action, v.Sync.Percent, time.Duration(v.Sync.ETASeconds)*time.Second
			}
			s.Volumes = append(s.Volumes, lv)
		}

		// Problems: where they are, never who (no user names on the display).
		for _, p := range mon.Active() {
			if p.Severity == "info" {
				continue
			}
			parts := strings.Split(p.Key, ":")
			where := ""
			if len(parts) > 1 {
				where = parts[1]
				if b, ok := bayOf[where]; ok {
					where = b
				}
			}
			if p.Kind == "fan_failed" || strings.HasPrefix(p.Kind, "ups") {
				where = ""
			}
			s.Problems = append(s.Problems, lcd.Problem{Severity: p.Severity, Kind: p.Kind, Where: where})
		}

		for _, sensor := range health.Sensors(hwmon) {
			switch {
			case sensor.Kind == "temp" && strings.Contains(strings.ToLower(sensor.Chip+sensor.Label), "core"):
				s.CPUTemp = max(s.CPUTemp, int(sensor.Value))
			case sensor.Kind == "fan" && s.FanRPM < 0:
				s.FanRPM = int(sensor.Value)
			}
		}
		if u := pw.UPSStatus(ctx); u.Enabled && u.Error == "" {
			s.UPS = lcd.UPS{OnBattery: u.OnBattery, Charge: u.Charge, RuntimeMin: u.RuntimeSec / 60}
		}
		if state.NetworkPending != nil {
			s.NetPending = state.NetworkPending.Deadline
		}
		for _, d := range loc.Active() {
			s.Locating = append(s.Locating, bayOf[d])
		}
		s.Button.Running, s.Button.Cur, s.Button.Total = btn.Progress()
		if list := jm.List(); len(list) > 0 && list[0].Status == jobs.Running {
			s.Job = list[0].Title
		}
		if t := state.Power.Timer; t.Off.Kind != "" {
			s.PowerOff = fmt.Sprintf("%02d:%02d", t.Off.Hour, t.Off.Minute)
			s.PowerOn = fmt.Sprintf("%02d:%02d", t.OnHour, t.OnMinute)
		}
		return s
	}
}

// primaryIPv4: the address people type to reach the NAS (bond0 first, then
// physical interfaces).
func primaryIPv4() string {
	ifaces, _ := net.Interfaces()
	best := ""
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				if ifc.Name == "bond0" {
					return ipn.IP.String()
				}
				if best == "" {
					best = ipn.IP.String()
				}
			}
		}
	}
	return best
}
