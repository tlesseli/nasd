package volumes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"readynas/internal/disks"
	"readynas/internal/run"
)

type CreateRequest struct {
	Name       string   `json:"name"`
	Level      string   `json:"level"`
	Filesystem string   `json:"filesystem"`
	Disks      []string `json:"disks"` // ["sda", "sdb", ...]
}

// ValidateCreate checks a request before anything is touched, and returns
// the selected disks. It runs synchronously so the UI gets errors at once,
// instead of a job that fails after wiping half the disks.
func (s *Service) ValidateCreate(req CreateRequest, all []disks.Disk, vols []Volume) ([]disks.Disk, error) {
	if !nameRe.MatchString(req.Name) {
		return nil, errors.New("name must be 1-16 characters: lowercase letters, digits, - or _, starting with a letter")
	}
	for _, v := range vols {
		if v.Name == req.Name {
			return nil, fmt.Errorf("a volume named %q already exists", req.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(s.MountRoot, req.Name)); err == nil {
		return nil, fmt.Errorf("%s already exists", filepath.Join(s.MountRoot, req.Name))
	}
	lvl, ok := Levels[req.Level]
	if !ok {
		return nil, fmt.Errorf("unknown RAID level %q", req.Level)
	}
	if !slices.Contains(Filesystems, req.Filesystem) {
		return nil, fmt.Errorf("unknown filesystem %q", req.Filesystem)
	}
	if len(req.Disks) < lvl.MinDisks {
		return nil, fmt.Errorf("%s needs at least %d disks", req.Level, lvl.MinDisks)
	}
	if lvl.Even && len(req.Disks)%2 != 0 {
		return nil, fmt.Errorf("%s needs an even number of disks", req.Level)
	}
	members := MemberDisks(vols)
	var selected []disks.Disk
	for _, name := range req.Disks {
		if slices.ContainsFunc(selected, func(d disks.Disk) bool { return d.Name == name }) {
			return nil, fmt.Errorf("disk %s selected twice", name)
		}
		d, err := freeDisk(name, all, members)
		if err != nil {
			return nil, err
		}
		selected = append(selected, d)
	}
	return selected, nil
}

// freeDisk finds a disk that may be used for a new volume: a data disk with
// nothing on it.
func freeDisk(name string, all []disks.Disk, members map[string]string) (disks.Disk, error) {
	i := slices.IndexFunc(all, func(d disks.Disk) bool { return d.Name == name })
	if i < 0 {
		return disks.Disk{}, fmt.Errorf("disk %s not found", name)
	}
	d := all[i]
	switch {
	case d.Role != "data":
		return d, fmt.Errorf("%s holds the operating system", name)
	case members[name] != "":
		return d, fmt.Errorf("%s belongs to volume %s", name, members[name])
	case d.Usage != "" || len(d.Partitions) > 0:
		return d, fmt.Errorf("%s is not empty (%s): erase it first", name, orDefault(d.Usage, "partitioned"))
	}
	return d, nil
}

// Create builds the volume. Each step is a plain command you could type
// yourself; the job log shows them all.
func (s *Service) Create(ctx context.Context, r run.Func, req CreateRequest, selected []disks.Disk) error {
	// Partition sizes must match across members: RAID uses the smallest one.
	smallest := selected[0].SizeBytes
	for _, d := range selected {
		smallest = min(smallest, d.SizeBytes)
	}
	var parts []string
	for _, d := range selected {
		p, err := s.partition(ctx, r, d, smallest)
		if err != nil {
			return err
		}
		parts = append(parts, p)
	}

	md := "/dev/md/" + req.Name
	args := []string{"--create", md, "--run", "--metadata=1.2", "--name=" + req.Name,
		"--level=" + req.Level, "--raid-devices=" + strconv.Itoa(len(parts))}
	if req.Level != "raid0" {
		// A write-intent bitmap: a disk pulled out and put back only
		// receives what changed meanwhile (see ReAdd).
		args = append(args, "--bitmap=internal")
	}
	if _, err := r(ctx, "mdadm", append(args, parts...)...); err != nil {
		return err
	}

	// The filesystem can be created right away: the kernel syncs the RAID
	// in the background and the volume is usable meanwhile (just slower).
	switch req.Filesystem {
	case "ext4":
		// -m 0: no blocks reserved for root, it's a data volume.
		// -O quota: per-user quotas ready from the start (enforced via the
		// usrquota mount option in fstab).
		if _, err := r(ctx, "mkfs.ext4", "-F", "-q", "-m", "0", "-O", "quota", "-E", "quotatype=usrquota", "-L", req.Name, md); err != nil {
			return err
		}
	case "btrfs":
		if _, err := r(ctx, "mkfs.btrfs", "-f", "-L", req.Name, md); err != nil {
			return err
		}
	}

	out, err := r(ctx, "blkid", "-s", "UUID", "-o", "value", md)
	if err != nil {
		return err
	}
	uuid := strings.TrimSpace(string(out))
	if uuid == "" {
		return errors.New("blkid returned no filesystem UUID")
	}

	mnt := filepath.Join(s.MountRoot, req.Name)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := s.setFstab(req.Name, fstabLine(uuid, mnt, req.Filesystem)); err != nil {
		return err
	}
	// systemd turns fstab into mount units: tell it fstab changed.
	if _, err := r(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	// Ask systemd to mount it (rather than calling mount ourselves) so the
	// mount belongs to the system, exactly like it will at the next boot.
	unit, err := mountUnit(ctx, r, mnt)
	if err != nil {
		return err
	}
	if _, err := r(ctx, "systemctl", "start", unit); err != nil {
		return err
	}
	return s.saveMdadmConf(ctx, r)
}

// mountUnit: systemd names mount units after their path, with an escaping
// scheme: /data/my-files -> data-my\x2dfiles.mount. Let systemd compute it.
func mountUnit(ctx context.Context, r run.Func, mnt string) (string, error) {
	out, err := r(ctx, "systemd-escape", "--path", "--suffix=mount", mnt)
	unit := strings.TrimSpace(string(out))
	if err == nil && unit == "" {
		err = fmt.Errorf("systemd-escape returned nothing for %s", mnt)
	}
	return unit, err
}

// prepareDisk partitions a disk that joins volume v and returns the partition
// for v. When v's disks also carry other arrays on other partitions (the
// system and swap, RAIDiator's layout), the new disk gets the same partitions
// as a healthy member and joins those arrays too: a replaced disk then
// restores every copy, not just the data.
func (s *Service) prepareDisk(ctx context.Context, r run.Func, v Volume, d disks.Disk, size int64) (string, error) {
	var src Member
	for _, m := range v.Members {
		if m.State == "in_sync" && m.Device != "/dev/"+m.Disk {
			src = m
			break
		}
	}
	all, err := s.List(ctx)
	if err != nil {
		return "", err
	}
	type join struct {
		array string
		part  int
	}
	var joins []join
	for _, o := range all {
		if o.Device == v.Device {
			continue
		}
		for _, m := range o.Members {
			if src.Disk != "" && m.Disk == src.Disk {
				joins = append(joins, join{o.Device, partNumber(m.Device)})
			}
		}
	}
	if len(joins) == 0 {
		return s.partition(ctx, r, d, size)
	}

	// Copy the partition positions of the healthy member's disk.
	out, err := r(ctx, "sfdisk", "--json", "/dev/"+src.Disk)
	if err != nil {
		return "", err
	}
	var doc struct {
		PartitionTable struct {
			SectorSize int64 `json:"sectorsize"`
			Partitions []struct {
				Node  string `json:"node"`
				Start int64  `json:"start"`
				Size  int64  `json:"size"`
			} `json:"partitions"`
		} `json:"partitiontable"`
	}
	if err := json.Unmarshal(out, &doc); err != nil || len(doc.PartitionTable.Partitions) == 0 {
		return "", fmt.Errorf("cannot read the partitions of %s", src.Disk)
	}
	pt := doc.PartitionTable
	if pt.SectorSize == 0 {
		pt.SectorSize = 512
	}
	args := []string{"-s", d.Path, "--", "mklabel", "gpt"}
	var nums []int
	for _, p := range pt.Partitions {
		if (p.Start+p.Size)*pt.SectorSize > d.SizeBytes {
			return "", fmt.Errorf("%s is too small: it must be at least as large as %s", d.Name, src.Disk)
		}
		n := partNumber(p.Node)
		nums = append(nums, n)
		args = append(args, "mkpart", "nas", fmt.Sprintf("%ds", p.Start), fmt.Sprintf("%ds", p.Start+p.Size-1),
			"set", strconv.Itoa(len(nums)), "raid", "on")
	}
	if _, err := r(ctx, "wipefs", "-a", d.Path); err != nil {
		return "", err
	}
	if _, err := r(ctx, "parted", args...); err != nil {
		return "", err
	}
	if _, err := r(ctx, "udevadm", "settle"); err != nil {
		return "", err
	}
	for i := range nums {
		// The partitions are created in order: the i-th is number i+1.
		if _, err := r(ctx, "wipefs", "-a", partitionPath(d.Path, i+1)); err != nil {
			return "", err
		}
	}
	at := func(n int) string { // new disk's partition at the same position as the source's partition n
		for i, x := range nums {
			if x == n {
				return partitionPath(d.Path, i+1)
			}
		}
		return ""
	}
	for _, j := range joins {
		if p := at(j.part); p != "" {
			// Errors are logged but don't stop the data volume's repair.
			r(ctx, "mdadm", j.array, "--add", p)
		}
	}
	p := at(partNumber(src.Device))
	if p == "" {
		return "", fmt.Errorf("no partition for %s on %s", v.Name, d.Name)
	}
	return p, nil
}

// partNumber: /dev/sda3 -> 3, /dev/nvme0n1p2 -> 2.
func partNumber(dev string) int {
	i := len(dev)
	for i > 0 && dev[i-1] >= '0' && dev[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(dev[i:])
	return n
}

// partition wipes a disk and creates a single GPT partition flagged "raid".
// Why a partition rather than the whole disk? Other OSes (and installers)
// see a partitioned disk as "in use" instead of offering to format it, and
// we can leave a little room at the end so a replacement disk that is a few
// MB smaller still fits.
func (s *Service) partition(ctx context.Context, r run.Func, d disks.Disk, sizeBytes int64) (string, error) {
	const MiB = 1 << 20
	end := sizeBytes/MiB - 64 // 64 MiB margin
	if end < 128 {
		return "", fmt.Errorf("%s is too small", d.Name)
	}
	if _, err := r(ctx, "wipefs", "-a", d.Path); err != nil {
		return "", err
	}
	if _, err := r(ctx, "parted", "-s", d.Path, "--", "mklabel", "gpt",
		"mkpart", "nas", "1MiB", fmt.Sprintf("%dMiB", end), "set", "1", "raid", "on"); err != nil {
		return "", err
	}
	if _, err := r(ctx, "udevadm", "settle"); err != nil { // wait for /dev/sdX1 to appear
		return "", err
	}
	p := partitionPath(d.Path, 1)
	// A RAID superblock from an earlier life can sit at the exact same
	// offset as the new partition: wipe it so mdadm doesn't get confused.
	if _, err := r(ctx, "wipefs", "-a", p); err != nil {
		return "", err
	}
	return p, nil
}

// partitionPath: /dev/sda -> /dev/sda1, /dev/nvme0n1 -> /dev/nvme0n1p1.
func partitionPath(disk string, n int) string {
	if unicode.IsDigit(rune(disk[len(disk)-1])) {
		return fmt.Sprintf("%sp%d", disk, n)
	}
	return fmt.Sprintf("%s%d", disk, n)
}

// Delete destroys a volume and all its data.
func (s *Service) Delete(ctx context.Context, r run.Func, v Volume) error {
	if v.Mountpoint != "" {
		// systemd tracks every mount as a unit, even ones it didn't create.
		unit, err := mountUnit(ctx, r, v.Mountpoint)
		if err != nil {
			return err
		}
		if _, err := r(ctx, "systemctl", "stop", unit); err != nil {
			return fmt.Errorf("cannot unmount %s, is something using it? %w", v.Mountpoint, err)
		}
	}
	if err := s.setFstab(v.Name, ""); err != nil {
		return err
	}
	r(ctx, "systemctl", "daemon-reload")
	os.Remove(filepath.Join(s.MountRoot, v.Name)) // only succeeds if empty, which it is once unmounted

	// Other arrays can live on other partitions of the same disks (RAIDiator
	// put its system, swap and data on partitions 1, 2 and 3 of every disk):
	// those disks keep their partition table.
	all, err := s.List(ctx)
	if err != nil {
		return fmt.Errorf("cannot check which disks other volumes use: %w", err)
	}
	shared := map[string]bool{}
	for _, o := range all {
		if o.Device == v.Device {
			continue
		}
		for _, m := range o.Members {
			shared[m.Disk] = true
		}
	}

	if v.VG != "" {
		// LVM sits between the RAID and the filesystem: release it first,
		// otherwise the array is busy.
		if _, err := r(ctx, "vgchange", "-an", v.VG); err != nil {
			return err
		}
	}
	if _, err := r(ctx, "mdadm", "--stop", v.Device); err != nil {
		return err
	}
	// Erase each member so the array can never be re-assembled. Errors are
	// logged but don't stop us: a dead disk shouldn't block the cleanup.
	for _, m := range v.Members {
		r(ctx, "mdadm", "--zero-superblock", m.Device)
		r(ctx, "wipefs", "-a", m.Device)
		if m.Device != "/dev/"+m.Disk && !shared[m.Disk] {
			r(ctx, "wipefs", "-a", "/dev/"+m.Disk)
		}
	}
	return s.saveMdadmConf(ctx, r)
}

// ValidateAddDisk: the disk must be free; RAID0 can't take extra disks.
func ValidateAddDisk(v Volume, name string, all []disks.Disk, vols []Volume) (disks.Disk, error) {
	if v.Level == "raid0" {
		return disks.Disk{}, errors.New("RAID 0 has no redundancy: disks can't be added or replaced")
	}
	return freeDisk(name, all, MemberDisks(vols))
}

// AddDisk adds a disk to a volume. If the volume is degraded the disk
// replaces the missing one and a rebuild starts; otherwise it becomes a hot
// spare that takes over automatically when a disk fails.
func (s *Service) AddDisk(ctx context.Context, r run.Func, v Volume, d disks.Disk) error {
	size, err := s.memberSize(v)
	if err != nil {
		return err
	}
	if d.SizeBytes < size {
		return fmt.Errorf("%s is too small for this volume", d.Name)
	}
	p, err := s.prepareDisk(ctx, r, v, d, size)
	if err != nil {
		return err
	}
	if _, err := r(ctx, "mdadm", v.Device, "--add", p); err != nil {
		return err
	}
	return s.saveMdadmConf(ctx, r)
}

// memberSize returns the disk size the volume was built for: the size of
// one member's whole disk (its partition + the margins).
func (s *Service) memberSize(v Volume) (int64, error) {
	for _, m := range v.Members {
		if n := readInt(filepath.Join(s.Sys, "block", m.Disk), "size"); n > 0 {
			return int64(n) * 512, nil
		}
	}
	return 0, errors.New("cannot determine member disk size")
}

// ValidateRemoveDisk refuses removals that would destroy the volume.
func ValidateRemoveDisk(v Volume, disk string) (Member, error) {
	i := slices.IndexFunc(v.Members, func(m Member) bool { return m.Disk == disk })
	if i < 0 {
		return Member{}, fmt.Errorf("%s is not part of volume %s", disk, v.Name)
	}
	m := v.Members[i]
	lvl, ok := Levels[v.Level]
	healthy := strings.Contains(m.State, "in_sync") && !strings.Contains(m.State, "faulty")
	if healthy && (!ok || v.Degraded+1 > lvl.Tolerates(v.RaidDisks)) {
		return m, fmt.Errorf("removing %s would destroy volume %s", disk, v.Name)
	}
	return m, nil
}

// RemoveDisk marks a member as failed and takes it out of the array, then
// wipes it so it shows up as a free disk.
func (s *Service) RemoveDisk(ctx context.Context, r run.Func, v Volume, m Member) error {
	if !strings.Contains(m.State, "faulty") {
		if _, err := r(ctx, "mdadm", v.Device, "--fail", m.Device); err != nil {
			return err
		}
	}
	if _, err := r(ctx, "mdadm", v.Device, "--remove", m.Device); err != nil {
		return err
	}
	r(ctx, "mdadm", "--zero-superblock", m.Device)
	r(ctx, "wipefs", "-a", m.Device)
	r(ctx, "wipefs", "-a", "/dev/"+m.Disk)
	return s.saveMdadmConf(ctx, r)
}

// Rejoin is one partition going back into its array.
type Rejoin struct {
	Array  string // /dev/md126
	Name   string // data, system, swap...
	Device string // /dev/sdc3
}

// ValidateReAdd lists what a returning disk puts back: its partition of v,
// plus its partitions of the other arrays (the system and swap copies, on
// RAIDiator's layout). The disk must be a data disk.
func ValidateReAdd(v Volume, disk string, all []disks.Disk, vols []Volume) ([]Rejoin, error) {
	i := slices.IndexFunc(all, func(d disks.Disk) bool { return d.Name == disk })
	if i < 0 {
		return nil, fmt.Errorf("disk %s not found", disk)
	}
	if all[i].Role != "data" {
		return nil, fmt.Errorf("%s is not a data disk", disk)
	}
	if !slices.ContainsFunc(v.Returning, func(r Returning) bool { return r.Disk == disk }) {
		return nil, fmt.Errorf("%s was not a member of volume %s, or the volume isn't missing a disk", disk, v.Name)
	}
	var plan []Rejoin
	for _, o := range vols {
		for _, r := range o.Returning {
			if r.Disk == disk {
				plan = append(plan, Rejoin{Array: o.Device, Name: o.Name, Device: r.Device})
			}
		}
	}
	// The small system arrays first: their copies are back within a minute.
	// The kernel rebuilds arrays sharing a disk one after the other anyway.
	sort.SliceStable(plan, func(a, b int) bool { return plan[a].Array != v.Device && plan[b].Array == v.Device })
	return plan, nil
}

// ReAdd puts a returning disk's partitions back into their arrays.
// "mdadm --re-add" takes the member back in its old place: with a bitmap,
// only the regions written while it was out are copied. Without a bitmap
// (or if the disk is too far behind) the kernel refuses; the partition then
// joins as a new member, which means a full rebuild. Its old superblock must
// be cleared first, or mdadm refuses to turn a former member into a spare.
func (s *Service) ReAdd(ctx context.Context, r run.Func, logf func(string, ...any), plan []Rejoin) error {
	var errs []error
	for _, p := range plan {
		if _, err := r(ctx, "mdadm", p.Array, "--re-add", p.Device); err == nil {
			logf("%s is back in %s: only the changes are copied", p.Device, p.Name)
			continue
		}
		logf("%s can't resume its place in %s: it joins as a new disk (full rebuild)", p.Device, p.Name)
		if _, err := r(ctx, "mdadm", "--zero-superblock", p.Device); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, err := r(ctx, "mdadm", p.Array, "--add", p.Device); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ValidateBitmap: a bitmap needs redundancy, and mdadm can't add one during
// a sync.
func ValidateBitmap(v Volume) error {
	switch {
	case v.Bitmap:
		return errors.New("this volume already has a bitmap")
	case v.Level == "raid0":
		return errors.New("RAID 0 has no redundancy: nothing to rebuild")
	case v.Sync != nil:
		return fmt.Errorf("wait for the current %s to finish", v.Sync.Action)
	}
	return nil
}

// AddBitmap adds a write-intent bitmap to a running array: it fits in the
// space mdadm reserved next to the superblock, no data moves, and the
// volume stays in use.
func (s *Service) AddBitmap(ctx context.Context, r run.Func, v Volume) error {
	_, err := r(ctx, "mdadm", "--grow", v.Device, "--bitmap=internal")
	return err
}

// ValidateExpand checks that a volume can grow by one disk. disk is either a
// free disk or the volume's current hot spare. Returns nil for a spare.
func ValidateExpand(v Volume, disk string, all []disks.Disk, vols []Volume) (*disks.Disk, error) {
	switch {
	case v.Level != "raid5" && v.Level != "raid6":
		return nil, errors.New("only RAID 5 and RAID 6 volumes can be expanded")
	case v.Status != "healthy":
		return nil, fmt.Errorf("volume is %s: wait until it is healthy", v.Status)
	}
	for _, m := range v.Members {
		if m.Disk == disk && strings.Contains(m.State, "spare") && m.Slot < 0 {
			return nil, nil // reuse the hot spare
		}
	}
	d, err := freeDisk(disk, all, MemberDisks(vols))
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Expand grows a RAID 5/6 by one disk. Three steps:
//  1. add the disk (unless it's already the spare),
//  2. reshape: the kernel moves every stripe to spread data over N+1 disks.
//     This rewrites the whole volume and takes hours on big disks; the
//     volume stays online and redundant meanwhile,
//  3. grow the filesystem into the new space.
func (s *Service) Expand(ctx context.Context, r run.Func, logf func(string, ...any), v Volume, d *disks.Disk) error {
	if d != nil {
		size, err := s.memberSize(v)
		if err != nil {
			return err
		}
		if d.SizeBytes < size {
			return fmt.Errorf("%s is too small for this volume", d.Name)
		}
		p, err := s.prepareDisk(ctx, r, v, *d, size)
		if err != nil {
			return err
		}
		if _, err := r(ctx, "mdadm", v.Device, "--add", p); err != nil {
			return err
		}
	}
	if _, err := r(ctx, "mdadm", "--grow", v.Device, "--raid-devices="+strconv.Itoa(v.RaidDisks+1)); err != nil {
		return err
	}
	if err := s.waitSync(ctx, logf, filepath.Base(v.Device)); err != nil {
		return err
	}
	if err := s.saveMdadmConf(ctx, r); err != nil {
		return err
	}
	// Re-read the volume: its size changed.
	grown, ok := s.readSysfs(filepath.Base(v.Device))
	if !ok {
		return errors.New("volume disappeared during the reshape")
	}
	grown.Mountpoint, grown.Filesystem, grown.VG, grown.LV = v.Mountpoint, v.Filesystem, v.VG, v.LV
	return s.GrowFS(ctx, r, grown)
}

// waitSync polls sysfs until the array is idle, logging every 5%.
func (s *Service) waitSync(ctx context.Context, logf func(string, ...any), dev string) error {
	lastLogged := -5.0
	for {
		v, ok := s.readSysfs(dev)
		if !ok {
			return errors.New("array disappeared")
		}
		if v.Sync == nil {
			logf("reshape finished")
			return nil
		}
		if v.Sync.Percent >= lastLogged+5 {
			lastLogged = v.Sync.Percent
			logf("%s: %.0f%%, about %s left", v.Sync.Action, v.Sync.Percent, (time.Duration(v.Sync.ETASeconds) * time.Second).String())
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stopped waiting (the reshape continues in the kernel; use \"Grow filesystem\" once it's done): %w", ctx.Err())
		case <-time.After(s.pollInterval()):
		}
	}
}

func (s *Service) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return 5 * time.Second
}

// GrowFS extends the filesystem to fill its volume. Both ext4 and btrfs can
// grow while mounted and in use.
func (s *Service) GrowFS(ctx context.Context, r run.Func, v Volume) error {
	if v.Mountpoint == "" {
		return errors.New("the volume must be mounted")
	}
	if v.LV != "" {
		// Through LVM: the physical volume (the array) grows first, then the
		// logical volume takes the free space, then the filesystem.
		if _, err := r(ctx, "pvresize", v.Device); err != nil {
			return err
		}
		if out, err := r(ctx, "lvextend", "-l", "+100%FREE", v.LV); err != nil && !strings.Contains(string(out), "matches existing size") {
			return err
		}
	}
	var err error
	switch v.Filesystem {
	case "ext4":
		_, err = r(ctx, "resize2fs", v.FSDevice())
	case "btrfs":
		_, err = r(ctx, "btrfs", "filesystem", "resize", "max", v.Mountpoint)
	default:
		err = fmt.Errorf("don't know how to grow %q", v.Filesystem)
	}
	return err
}

// ValidateErase: only data disks that no active volume uses.
func ValidateErase(name string, all []disks.Disk, vols []Volume) (disks.Disk, error) {
	i := slices.IndexFunc(all, func(d disks.Disk) bool { return d.Name == name })
	if i < 0 {
		return disks.Disk{}, fmt.Errorf("disk %s not found", name)
	}
	d := all[i]
	if d.Role != "data" {
		return d, fmt.Errorf("%s holds the operating system", name)
	}
	if v := MemberDisks(vols)[name]; v != "" {
		return d, fmt.Errorf("%s belongs to volume %s: delete the volume or remove the disk from it first", name, v)
	}
	return d, nil
}

// EraseDisk removes every signature (partition table, RAID superblocks,
// filesystems) so the disk is blank. wipefs refuses devices that are in
// use, a last safety net.
func (s *Service) EraseDisk(ctx context.Context, r run.Func, d disks.Disk) error {
	for _, p := range d.Partitions {
		r(ctx, "mdadm", "--zero-superblock", p)
		if _, err := r(ctx, "wipefs", "-a", p); err != nil {
			return err
		}
	}
	if _, err := r(ctx, "wipefs", "-a", d.Path); err != nil {
		return err
	}
	_, err := r(ctx, "udevadm", "settle")
	return err
}

// ------------------------------------------------------- config files

// fstab line. nofail: boot continues even if the volume is broken (otherwise
// a dead array drops the NAS into an emergency shell you can't reach without
// a screen). Mounted by filesystem UUID, which never changes, unlike md127.
func fstabLine(uuid, mnt, fs string) string {
	pass, opts := 2, "defaults,nofail,x-systemd.device-timeout=30s" // fsck at boot for ext4
	if fs == "btrfs" {
		pass = 0 // btrfs checks itself
	} else {
		opts += ",usrquota" // enforce per-user quotas
	}
	return fmt.Sprintf("UUID=%s %s %s %s 0 %d", uuid, mnt, fs, opts, pass)
}

// setFstab replaces (or removes, if line is "") the entry nasd owns for a
// volume. Our entries are marked by a comment line just above them, so
// entries you add by hand are left alone.
func (s *Service) setFstab(name, line string) error {
	data, err := os.ReadFile(s.Fstab)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	marker := "# nasd:volume=" + name
	var out []string
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		if lines[i] == marker {
			i++ // skip the marker and the entry below it
			continue
		}
		out = append(out, lines[i])
	}
	if line != "" {
		out = append(out, marker, line)
	}
	return writeFileAtomic(s.Fstab, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}

// saveMdadmConf regenerates the ARRAY lines of mdadm.conf from the arrays
// that exist now, then rebuilds the initramfs (the boot-time mini system,
// which carries a copy of mdadm.conf). Arrays would still assemble without
// it, but possibly under random names like md127.
func (s *Service) saveMdadmConf(ctx context.Context, r run.Func) error {
	scan, err := r(ctx, "mdadm", "--detail", "--scan")
	if err != nil {
		return err
	}
	data, err := os.ReadFile(s.MdadmConf)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if !strings.HasPrefix(l, "ARRAY ") {
			out = append(out, l)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(string(scan)), "\n") {
		if strings.HasPrefix(l, "ARRAY ") {
			out = append(out, l)
		}
	}
	if err := writeFileAtomic(s.MdadmConf, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	_, err = r(ctx, "update-initramfs", "-u")
	return err
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nasd-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
