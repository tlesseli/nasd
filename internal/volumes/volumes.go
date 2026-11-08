// Package volumes manages RAID volumes: Linux software RAID (mdadm) with a
// filesystem on top, mounted under /data/<name>.
//
// Design rule: the system is the source of truth. nasd doesn't keep its own
// list of volumes; it reads the kernel's view (/sys/block/md*/md/) every time.
// So if you create or break an array by hand with mdadm, the UI shows it.
//
// The layers of a volume, bottom to top:
//
//	/dev/sda ─ GPT partition /dev/sda1 ─┐
//	/dev/sdb ─ GPT partition /dev/sdb1 ─┼─ /dev/md/<name> (RAID) ─ ext4/btrfs ─ /data/<name>
//	/dev/sdc ─ GPT partition /dev/sdc1 ─┘
package volumes

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"readynas/internal/run"
)

type Member struct {
	Device string `json:"device"` // /dev/sda1
	Disk   string `json:"disk"`   // sda
	State  string `json:"state"`  // in_sync, faulty, spare, ...
	Slot   int    `json:"slot"`   // position in the array, -1 for spares
}

type Sync struct {
	Action     string  `json:"action"` // resync, recover, check, reshape...
	Percent    float64 `json:"percent"`
	SpeedKBs   int64   `json:"speedKBs"`
	ETASeconds int64   `json:"etaSeconds"`
}

type Volume struct {
	Name       string   `json:"name"`
	Device     string   `json:"device"` // /dev/md127
	Level      string   `json:"level"`  // raid0, raid1, raid5, raid6, raid10
	Status     string   `json:"status"` // healthy, syncing, rebuilding, degraded, failed, inactive...
	SizeBytes  int64    `json:"sizeBytes"`
	RaidDisks  int      `json:"raidDisks"`
	Degraded   int      `json:"degraded"` // number of missing members
	Members    []Member `json:"members"`
	Sync       *Sync    `json:"sync"`
	Filesystem string   `json:"filesystem"`
	UUID       string   `json:"uuid"` // filesystem UUID
	Mountpoint string   `json:"mountpoint"`
	TotalBytes int64    `json:"totalBytes"` // filesystem usage, if mounted
	UsedBytes  int64    `json:"usedBytes"`
	AvailBytes int64    `json:"availBytes"`
	// The filesystem is clearly smaller than the volume (e.g. a reshape
	// finished while nasd wasn't watching): offer to grow it.
	CanGrowFS bool `json:"canGrowFS"`
	// Result of the last consistency check (see maintenance.go).
	LastSyncAction string `json:"lastSyncAction"` // check, repair, resync, recover...
	MismatchCount  int64  `json:"mismatchCount"`  // blocks that didn't match their parity/mirror
}

// Level describes a RAID level: minimum disks, usable capacity (in number
// of disks) and how many disks can fail without losing data.
type Level struct {
	MinDisks  int
	Even      bool
	Usable    func(n int) int
	Tolerates func(n int) int
}

var Levels = map[string]Level{
	"raid0":  {MinDisks: 2, Usable: func(n int) int { return n }, Tolerates: func(n int) int { return 0 }},
	"raid1":  {MinDisks: 2, Usable: func(n int) int { return 1 }, Tolerates: func(n int) int { return n - 1 }},
	"raid5":  {MinDisks: 3, Usable: func(n int) int { return n - 1 }, Tolerates: func(n int) int { return 1 }},
	"raid6":  {MinDisks: 4, Usable: func(n int) int { return n - 2 }, Tolerates: func(n int) int { return 2 }},
	"raid10": {MinDisks: 4, Even: true, Usable: func(n int) int { return n / 2 }, Tolerates: func(n int) int { return 1 }},
}

var Filesystems = []string{"ext4", "btrfs"}

// Names become device names, ext4 labels (max 16 chars) and directories:
// keep them boring.
var nameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,15}$`)

type Service struct {
	Sys          string // "/sys", a temp dir in tests
	Fstab        string // "/etc/fstab"
	MdadmConf    string // "/etc/mdadm/mdadm.conf"
	MountRoot    string // "/data"
	Run          run.Func
	SkipStatfs   bool          // tests
	PollInterval time.Duration // tests; default 5 s
}

func NewService(r run.Func) *Service {
	return &Service{Sys: "/sys", Fstab: "/etc/fstab", MdadmConf: "/etc/mdadm/mdadm.conf", MountRoot: "/data", Run: r}
}

// ---------------------------------------------------------------- reading

func (s *Service) List(ctx context.Context) ([]Volume, error) {
	entries, err := os.ReadDir(filepath.Join(s.Sys, "block"))
	if err != nil {
		return nil, err
	}
	vols := []Volume{}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "md") {
			continue
		}
		v, ok := s.readSysfs(e.Name())
		if !ok {
			continue
		}
		s.fillNameAndFS(ctx, &v)
		vols = append(vols, v)
	}
	sort.Slice(vols, func(i, j int) bool { return vols[i].Name < vols[j].Name })
	return vols, nil
}

func (s *Service) Get(ctx context.Context, name string) (Volume, error) {
	vols, err := s.List(ctx)
	if err != nil {
		return Volume{}, err
	}
	for _, v := range vols {
		if v.Name == name {
			return v, nil
		}
	}
	return Volume{}, fmt.Errorf("volume %q not found", name)
}

// readSysfs builds a Volume from /sys/block/mdX/md/*, the kernel's live
// view of the array. Every file there holds a single value.
func (s *Service) readSysfs(dev string) (Volume, bool) {
	md := filepath.Join(s.Sys, "block", dev, "md")
	state := readStr(md, "array_state")
	if state == "" || state == "clear" { // unused md device node
		return Volume{}, false
	}
	v := Volume{
		Name:      dev,
		Device:    "/dev/" + dev,
		Level:     readStr(md, "level"),
		RaidDisks: readInt(md, "raid_disks"),
		Degraded:  readInt(md, "degraded"),
		SizeBytes: int64(readInt(filepath.Dir(md), "size")) * 512, // sysfs sizes are in 512-byte sectors
		Members:   []Member{},
	}

	// One dev-<name> directory per member device.
	devDirs, _ := filepath.Glob(filepath.Join(md, "dev-*"))
	for _, d := range devDirs {
		m := Member{
			Device: "/dev/" + strings.TrimPrefix(filepath.Base(d), "dev-"),
			State:  readStr(d, "state"),
			Slot:   -1,
		}
		if slot, err := strconv.Atoi(readStr(d, "slot")); err == nil {
			m.Slot = slot
		}
		// "block" links to the member's sysfs node, e.g. .../block/sda/sda1:
		// the parent directory is the physical disk.
		m.Disk = strings.TrimPrefix(m.Device, "/dev/")
		if target, err := os.Readlink(filepath.Join(d, "block")); err == nil {
			if parent := filepath.Base(filepath.Dir(target)); parent != "block" {
				m.Disk = parent
			}
		}
		v.Members = append(v.Members, m)
	}
	sort.Slice(v.Members, func(i, j int) bool { return v.Members[i].Disk < v.Members[j].Disk })

	v.LastSyncAction = readStr(md, "last_sync_action")
	v.MismatchCount = int64(readInt(md, "mismatch_cnt"))

	action := readStr(md, "sync_action")
	if action != "" && action != "idle" {
		v.Sync = &Sync{Action: action}
		// sync_completed is "<done> / <total>" in sectors, sync_speed is KiB/s.
		var done, total int64
		if _, err := fmt.Sscanf(readStr(md, "sync_completed"), "%d / %d", &done, &total); err == nil && total > 0 {
			v.Sync.Percent = float64(done) * 100 / float64(total)
			if speed := int64(readInt(md, "sync_speed")); speed > 0 {
				v.Sync.SpeedKBs = speed
				v.Sync.ETASeconds = (total - done) / 2 / speed
			}
		}
	}
	v.Status = status(v, state)
	return v, true
}

func status(v Volume, arrayState string) string {
	if arrayState == "inactive" {
		return "inactive"
	}
	if lvl, ok := Levels[v.Level]; ok && v.Degraded > lvl.Tolerates(v.RaidDisks) {
		return "failed"
	}
	action := ""
	if v.Sync != nil {
		action = v.Sync.Action
	}
	switch {
	case v.Degraded > 0 && action == "recover":
		return "rebuilding"
	case v.Degraded > 0:
		return "degraded"
	case action == "resync":
		return "syncing"
	case action == "reshape":
		return "reshaping"
	case action == "check" || action == "repair":
		return "checking"
	}
	return "healthy"
}

// fillNameAndFS asks mdadm for the array's name and lsblk for its filesystem.
func (s *Service) fillNameAndFS(ctx context.Context, v *Volume) {
	if out, err := s.Run(ctx, "mdadm", "--detail", "--export", v.Device); err == nil {
		kv := parseExport(string(out))
		switch {
		case kv["MD_DEVNAME"] != "":
			v.Name = kv["MD_DEVNAME"]
		case kv["MD_NAME"] != "": // "host:name"
			_, n, _ := strings.Cut(kv["MD_NAME"], ":")
			v.Name = n
		}
	}
	out, err := s.Run(ctx, "lsblk", "--json", "--bytes", "--nodeps", "-o", "FSTYPE,UUID,MOUNTPOINTS", v.Device)
	if err != nil {
		return
	}
	var doc struct {
		Blockdevices []struct {
			FSType      *string   `json:"fstype"`
			UUID        *string   `json:"uuid"`
			Mountpoints []*string `json:"mountpoints"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal(out, &doc) != nil || len(doc.Blockdevices) == 0 {
		return
	}
	b := doc.Blockdevices[0]
	v.Filesystem, v.UUID = deref(b.FSType), deref(b.UUID)
	for _, m := range b.Mountpoints {
		if m != nil && *m != "" {
			v.Mountpoint = *m
			break
		}
	}
	if v.Mountpoint != "" && !s.SkipStatfs {
		var st syscall.Statfs_t
		if syscall.Statfs(v.Mountpoint, &st) == nil {
			bs := int64(st.Bsize)
			v.TotalBytes = int64(st.Blocks) * bs
			v.AvailBytes = int64(st.Bavail) * bs
			v.UsedBytes = int64(st.Blocks-st.Bfree) * bs
			// Filesystems keep some space for metadata, so allow a 10% gap.
			v.CanGrowFS = v.Sync == nil && v.TotalBytes > 0 && float64(v.TotalBytes) < 0.9*float64(v.SizeBytes)
		}
	}
}

func parseExport(s string) map[string]string {
	kv := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			kv[k] = v
		}
	}
	return kv
}

// MemberDisks returns the set of disks used by any array.
func MemberDisks(vols []Volume) map[string]string {
	m := map[string]string{}
	for _, v := range vols {
		for _, mem := range v.Members {
			m[mem.Disk] = v.Name
		}
	}
	return m
}

// ---------------------------------------------------------------- helpers

func readStr(dir, file string) string {
	b, err := os.ReadFile(filepath.Join(dir, file))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(dir, file string) int {
	n, _ := strconv.Atoi(readStr(dir, file))
	return n
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
