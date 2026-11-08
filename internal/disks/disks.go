// Package disks lists the physical disks and their health.
//
// We don't talk to the hardware ourselves: we call the standard Linux tools
// with their JSON output (`lsblk --json`, `smartctl --json`) and parse it.
// That's the core pattern of the whole project: nasd is a thin, well-typed
// layer on top of proven tools.
package disks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"readynas/internal/run"
)

type Disk struct {
	Name       string   `json:"name"` // sda
	Path       string   `json:"path"` // /dev/sda
	Model      string   `json:"model"`
	Serial     string   `json:"serial"`
	SizeBytes  int64    `json:"sizeBytes"`
	Transport  string   `json:"transport"` // sata, usb, nvme, virtio...
	Rotational bool     `json:"rotational"`
	Role       string   `json:"role"`       // "system" (holds the OS) or "data"
	Usage      string   `json:"usage"`      // what's on it: "", "raid member", "ext4"...
	Partitions []string `json:"partitions"` // /dev/sda1...
	Health     *Health  `json:"health"`
}

type Health struct {
	Supported     bool   `json:"supported"`
	Passed        bool   `json:"passed"`
	TemperatureC  int    `json:"temperatureC"`
	PowerOnHours  int    `json:"powerOnHours"`
	Reallocated   int64  `json:"reallocatedSectors"`   // SMART 5
	Pending       int64  `json:"pendingSectors"`       // SMART 197
	Uncorrectable int64  `json:"uncorrectableSectors"` // SMART 198
	Error         string `json:"error,omitempty"`
	Standby       bool   `json:"standby"` // asleep (spun down): values are from before
}

// Runner executes a command and returns its stdout. Tests replace it with
// a fake that returns canned output, so they run without real disks.
type Runner = run.Func

type Service struct {
	run Runner

	// smartctl takes a moment per disk and wakes sleeping disks, so health
	// is cached instead of queried on every page refresh.
	mu        sync.Mutex
	healthAt  map[string]time.Time
	health    map[string]*Health
	healthTTL time.Duration
}

func NewService(run Runner) *Service {
	return &Service{run: run, health: map[string]*Health{}, healthAt: map[string]time.Time{}, healthTTL: 5 * time.Minute}
}

func (s *Service) List(ctx context.Context) ([]Disk, error) {
	out, err := s.run(ctx, "lsblk", "--json", "--bytes", "--tree",
		"-o", "NAME,PATH,SIZE,MODEL,SERIAL,TYPE,TRAN,ROTA,RO,FSTYPE,MOUNTPOINTS")
	if err != nil {
		return nil, fmt.Errorf("lsblk: %w", err)
	}
	list, err := parseLsblk(out)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Health = s.healthOf(ctx, list[i].Path)
	}
	return list, nil
}

// ---- lsblk ----

type lsblkDev struct {
	Name        string     `json:"name"`
	Path        string     `json:"path"`
	Size        flexInt    `json:"size"`
	Model       *string    `json:"model"`
	Serial      *string    `json:"serial"`
	Type        string     `json:"type"`
	Tran        *string    `json:"tran"`
	Rota        flexBool   `json:"rota"`
	RO          flexBool   `json:"ro"`
	FSType      *string    `json:"fstype"`
	Mountpoints []*string  `json:"mountpoints"`
	Children    []lsblkDev `json:"children"`
}

func parseLsblk(data []byte) ([]Disk, error) {
	var doc struct {
		Blockdevices []lsblkDev `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse lsblk: %w", err)
	}
	var disks []Disk
	for _, d := range doc.Blockdevices {
		// Read-only disks (CD-ROM, the VM's cloud-init seed...) can't store data.
		if d.Type != "disk" || bool(d.RO) || isVirtualDevice(d.Name) {
			continue
		}
		disk := Disk{
			Name:       d.Name,
			Path:       d.Path,
			Model:      strings.TrimSpace(str(d.Model)),
			Serial:     strings.TrimSpace(str(d.Serial)),
			SizeBytes:  int64(d.Size),
			Transport:  str(d.Tran),
			Rotational: bool(d.Rota),
			Role:       "data",
			Usage:      usage(d),
		}
		for _, c := range d.Children {
			if c.Type == "part" {
				disk.Partitions = append(disk.Partitions, c.Path)
			}
		}
		switch {
		case holdsSystem(d):
			disk.Role = "system"
		case disk.Transport == "usb":
			// External disks come and go: never part of a RAID, used for backups.
			disk.Role = "usb"
		}
		disks = append(disks, disk)
	}
	sort.Slice(disks, func(i, j int) bool { return disks[i].Name < disks[j].Name })
	return disks, nil
}

func isVirtualDevice(name string) bool {
	for _, p := range []string{"loop", "zram", "ram", "sr", "fd", "md", "dm-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// holdsSystem reports whether the OS root (/) lives on this disk, directly
// or through a partition / RAID / LVM device stacked on top of it.
func holdsSystem(d lsblkDev) bool {
	for _, m := range d.Mountpoints {
		if m != nil && (*m == "/" || *m == "/boot" || *m == "/boot/efi") {
			return true
		}
	}
	for _, c := range d.Children {
		if holdsSystem(c) {
			return true
		}
	}
	return false
}

func usage(d lsblkDev) string {
	fs := str(d.FSType)
	if fs == "" {
		for _, c := range d.Children {
			if f := str(c.FSType); f != "" {
				fs = f
				break
			}
		}
	}
	if fs == "linux_raid_member" {
		return "raid member"
	}
	if fs == "" && len(d.Children) > 0 {
		return "partitioned"
	}
	return fs
}

// ---- smartctl ----

func (s *Service) healthOf(ctx context.Context, path string) *Health {
	s.mu.Lock()
	if h, ok := s.health[path]; ok && time.Since(s.healthAt[path]) < s.healthTTL {
		s.mu.Unlock()
		return h
	}
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	// smartctl's exit code is a bit mask that is non-zero for many benign
	// reasons, so we ignore it and trust the JSON instead.
	// -n standby: if the disk is asleep, don't spin it up just to read
	// its health (that would defeat spin-down). Keep the last values.
	out, _ := s.run(ctx, "smartctl", "--json=c", "-n", "standby", "-i", "-H", "-A", path)
	var h *Health
	if strings.Contains(string(out), "STANDBY mode") {
		s.mu.Lock()
		prev := s.health[path]
		s.mu.Unlock()
		h = &Health{}
		if prev != nil {
			copy := *prev
			h = &copy
		}
		h.Standby = true
	} else {
		h = parseSmart(out)
	}

	s.mu.Lock()
	s.health[path], s.healthAt[path] = h, time.Now()
	s.mu.Unlock()
	return h
}

func parseSmart(data []byte) *Health {
	var doc struct {
		Smartctl struct {
			Messages []struct {
				String   string `json:"string"`
				Severity string `json:"severity"`
			} `json:"messages"`
		} `json:"smartctl"`
		SmartSupport *struct {
			Available bool `json:"available"`
			Enabled   bool `json:"enabled"`
		} `json:"smart_support"`
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current int `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours int `json:"hours"`
		} `json:"power_on_time"`
		ATAAttributes struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value int64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
	}
	if len(data) == 0 {
		return &Health{Error: "smartctl not available"}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return &Health{Error: "cannot parse smartctl output"}
	}
	h := &Health{
		TemperatureC: doc.Temperature.Current,
		PowerOnHours: doc.PowerOnTime.Hours,
	}
	if doc.SmartStatus != nil {
		h.Supported = true
		h.Passed = doc.SmartStatus.Passed
	} else {
		for _, m := range doc.Smartctl.Messages {
			if m.Severity == "error" {
				h.Error = m.String
				break
			}
		}
		if h.Error == "" {
			h.Error = "SMART not supported by this device"
		}
	}
	for _, a := range doc.ATAAttributes.Table {
		switch a.ID {
		case 5:
			h.Reallocated = a.Raw.Value
		case 197:
			h.Pending = a.Raw.Value
		case 198:
			h.Uncorrectable = a.Raw.Value
		}
	}
	return h
}

// ---- JSON helpers: lsblk's output types changed across versions ----

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(s == "true" || s == "1")
	return nil
}

type flexInt int64

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "null" || s == "" {
		*n = 0
		return nil
	}
	var v int64
	_, err := fmt.Sscan(s, &v)
	*n = flexInt(v)
	return err
}
