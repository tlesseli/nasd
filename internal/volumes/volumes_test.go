package volumes

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"readynas/internal/disks"
)

// fakeSys builds a tiny /sys tree: a degraded RAID5 md127 rebuilding onto
// sdd1, with sdb1 missing (faulty).
func fakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	md := filepath.Join(root, "block", "md127", "md")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "block", "md127", "size"), "8376320")
	for f, v := range map[string]string{
		"array_state": "clean", "level": "raid5", "raid_disks": "3", "degraded": "1",
		"sync_action": "recover", "sync_completed": "1000000 / 4000000", "sync_speed": "50000",
	} {
		write(filepath.Join(md, f), v)
	}
	for dev, st := range map[string][2]string{"sda1": {"in_sync", "0"}, "sdc1": {"in_sync", "2"}, "sdd1": {"spare", "1"}} {
		write(filepath.Join(md, "dev-"+dev, "state"), st[0])
		write(filepath.Join(md, "dev-"+dev, "slot"), st[1])
		os.Symlink("../../../devices/pci/ata1/block/"+dev[:3]+"/"+dev, filepath.Join(md, "dev-"+dev, "block"))
	}
	// An unused md device must be ignored.
	write(filepath.Join(root, "block", "md0", "md", "array_state"), "clear")
	write(filepath.Join(root, "block", "sda", "size"), "4194304")
	return root
}

type recorder struct {
	cmds   []string
	output map[string]string // command prefix -> stdout
}

func (r *recorder) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, cmd)
	for prefix, out := range r.output {
		if strings.HasPrefix(cmd, prefix) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

func newTestService(t *testing.T) (*Service, *recorder) {
	rec := &recorder{output: map[string]string{
		"mdadm --detail --export": "MD_LEVEL=raid5\nMD_DEVNAME=media\nMD_NAME=nas-dev:media\n",
		"lsblk":                   `{"blockdevices":[{"fstype":"ext4","uuid":"abcd-1234","mountpoints":["/data/media"]}]}`,
		"blkid":                   "1111-2222\n",
		"mdadm --detail --scan":   "ARRAY /dev/md/media metadata=1.2 UUID=x:y:z:w\n",
		"systemd-escape":          "data-media.mount\n",
	}}
	dir := t.TempDir()
	s := &Service{
		Sys:        fakeSys(t),
		Fstab:      filepath.Join(dir, "fstab"),
		MdadmConf:  filepath.Join(dir, "mdadm.conf"),
		MountRoot:  filepath.Join(dir, "data"),
		Run:        rec.run,
		SkipStatfs: true,
	}
	return s, rec
}

func TestList(t *testing.T) {
	s, _ := newTestService(t)
	vols, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 {
		t.Fatalf("want 1 volume (md0 is unused), got %d", len(vols))
	}
	v := vols[0]
	if v.Name != "media" || v.Level != "raid5" || v.Status != "rebuilding" || v.Mountpoint != "/data/media" {
		t.Errorf("volume: %+v", v)
	}
	if v.SizeBytes != 8376320*512 {
		t.Errorf("size %d", v.SizeBytes)
	}
	if v.Sync == nil || v.Sync.Percent != 25 || v.Sync.ETASeconds != 30 {
		// remaining 3,000,000 sectors = 1,500,000 KiB at 50,000 KiB/s = 30 s
		t.Errorf("sync: %+v", v.Sync)
	}
	if len(v.Members) != 3 || v.Members[0].Disk != "sda" || v.Members[2].State != "spare" || v.Members[2].Slot != 1 {
		t.Errorf("members: %+v", v.Members)
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		v     Volume
		state string
		want  string
	}{
		{Volume{Level: "raid5", RaidDisks: 3}, "clean", "healthy"},
		{Volume{Level: "raid5", RaidDisks: 3, Degraded: 1}, "clean", "degraded"},
		{Volume{Level: "raid5", RaidDisks: 3, Degraded: 2}, "clean", "failed"},
		{Volume{Level: "raid6", RaidDisks: 4, Degraded: 2}, "clean", "degraded"},
		{Volume{Level: "raid1", RaidDisks: 2, Sync: &Sync{Action: "resync"}}, "active", "syncing"},
		{Volume{Level: "raid1", RaidDisks: 2}, "inactive", "inactive"},
	}
	for _, c := range cases {
		if got := status(c.v, c.state); got != c.want {
			t.Errorf("%+v %s: got %s want %s", c.v, c.state, got, c.want)
		}
	}
}

var testDisks = []disks.Disk{
	{Name: "sda", Path: "/dev/sda", Role: "data", SizeBytes: 2 << 30},
	{Name: "sdb", Path: "/dev/sdb", Role: "data", SizeBytes: 2 << 30},
	{Name: "sdc", Path: "/dev/sdc", Role: "data", SizeBytes: 3 << 30},
	{Name: "sdd", Path: "/dev/sdd", Role: "data", Usage: "raid member", Partitions: []string{"/dev/sdd1"}},
	{Name: "vda", Path: "/dev/vda", Role: "system"},
}

func TestValidateCreate(t *testing.T) {
	s, _ := newTestService(t)
	bad := map[string]CreateRequest{
		"bad name":      {Name: "Media!", Level: "raid5", Filesystem: "ext4", Disks: []string{"sda", "sdb", "sdc"}},
		"too few disks": {Name: "m", Level: "raid5", Filesystem: "ext4", Disks: []string{"sda", "sdb"}},
		"odd raid10":    {Name: "m", Level: "raid10", Filesystem: "ext4", Disks: []string{"sda", "sdb", "sdc"}},
		"system disk":   {Name: "m", Level: "raid1", Filesystem: "ext4", Disks: []string{"sda", "vda"}},
		"used disk":     {Name: "m", Level: "raid1", Filesystem: "ext4", Disks: []string{"sda", "sdd"}},
		"duplicate":     {Name: "m", Level: "raid1", Filesystem: "ext4", Disks: []string{"sda", "sda"}},
		"unknown fs":    {Name: "m", Level: "raid1", Filesystem: "ntfs", Disks: []string{"sda", "sdb"}},
		"existing name": {Name: "media", Level: "raid1", Filesystem: "ext4", Disks: []string{"sda", "sdb"}},
		"no such disk":  {Name: "m", Level: "raid1", Filesystem: "ext4", Disks: []string{"sda", "sdz"}},
		"unknown level": {Name: "m", Level: "raid7", Filesystem: "ext4", Disks: []string{"sda", "sdb"}},
	}
	vols := []Volume{{Name: "media"}}
	for label, req := range bad {
		if _, err := s.ValidateCreate(req, testDisks, vols); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
	sel, err := s.ValidateCreate(CreateRequest{Name: "m", Level: "raid5", Filesystem: "btrfs", Disks: []string{"sda", "sdb", "sdc"}}, testDisks, vols)
	if err != nil || len(sel) != 3 {
		t.Fatalf("valid request refused: %v", err)
	}
}

func TestCreateCommands(t *testing.T) {
	s, rec := newTestService(t)
	os.WriteFile(s.Fstab, []byte("UUID=root / ext4 defaults 0 1\n"), 0o644)
	os.WriteFile(s.MdadmConf, []byte("HOMEHOST <system>\nARRAY /dev/md/old UUID=a\n"), 0o644)

	req := CreateRequest{Name: "media", Level: "raid5", Filesystem: "ext4", Disks: []string{"sda", "sdb", "sdc"}}
	if err := s.Create(context.Background(), rec.run, req, testDisks[:3]); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(rec.cmds, "\n")
	for _, want := range []string{
		// smallest disk is 2 GiB = 2048 MiB, minus the 64 MiB margin
		"parted -s /dev/sdc -- mklabel gpt mkpart nas 1MiB 1984MiB set 1 raid on",
		"mdadm --create /dev/md/media --run --metadata=1.2 --name=media --level=raid5 --raid-devices=3 /dev/sda1 /dev/sdb1 /dev/sdc1",
		"mkfs.ext4 -F -q -m 0 -L media /dev/md/media",
		"systemd-escape --path --suffix=mount " + filepath.Join(s.MountRoot, "media"),
		"systemctl start data-media.mount",
		"update-initramfs -u",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing command %q in:\n%s", want, all)
		}
	}

	fstab, _ := os.ReadFile(s.Fstab)
	if !strings.Contains(string(fstab), "UUID=root / ext4") || !strings.Contains(string(fstab), "# nasd:volume=media\nUUID=1111-2222 ") {
		t.Errorf("fstab:\n%s", fstab)
	}
	conf, _ := os.ReadFile(s.MdadmConf)
	if string(conf) != "HOMEHOST <system>\nARRAY /dev/md/media metadata=1.2 UUID=x:y:z:w\n" {
		t.Errorf("mdadm.conf:\n%s", conf)
	}

	// Removing the entry restores the original fstab.
	if err := s.setFstab("media", ""); err != nil {
		t.Fatal(err)
	}
	if fstab, _ := os.ReadFile(s.Fstab); string(fstab) != "UUID=root / ext4 defaults 0 1\n" {
		t.Errorf("fstab after removal:\n%q", fstab)
	}
}

func TestValidateRemoveDisk(t *testing.T) {
	v := Volume{Name: "media", Level: "raid5", RaidDisks: 3, Degraded: 1, Members: []Member{
		{Disk: "sda", State: "in_sync"}, {Disk: "sdc", State: "in_sync"}, {Disk: "sdd", State: "spare"},
	}}
	if _, err := ValidateRemoveDisk(v, "sda"); err == nil {
		t.Error("removing a healthy disk from a degraded RAID5 must be refused")
	}
	if _, err := ValidateRemoveDisk(v, "sdd"); err != nil {
		t.Errorf("removing a spare must be allowed: %v", err)
	}
	v.Degraded = 0
	if _, err := ValidateRemoveDisk(v, "sda"); err != nil {
		t.Errorf("removing one disk from a healthy RAID5 must be allowed: %v", err)
	}
	if _, err := ValidateRemoveDisk(Volume{Level: "raid0", RaidDisks: 2, Members: []Member{{Disk: "sda", State: "in_sync"}}}, "sda"); err == nil {
		t.Error("RAID0 members can't be removed")
	}
}

func TestPartitionPath(t *testing.T) {
	if p := partitionPath("/dev/sda", 1); p != "/dev/sda1" {
		t.Error(p)
	}
	if p := partitionPath("/dev/nvme0n1", 1); p != "/dev/nvme0n1p1" {
		t.Error(p)
	}
}

func TestValidateExpand(t *testing.T) {
	healthy := Volume{Name: "media", Level: "raid5", Status: "healthy", RaidDisks: 3, Members: []Member{
		{Disk: "sda", State: "in_sync", Slot: 0}, {Disk: "sdb", State: "in_sync", Slot: 1},
		{Disk: "sdc", State: "in_sync", Slot: 2}, {Disk: "sdd", State: "spare", Slot: -1},
	}}
	all := append([]disks.Disk{{Name: "sde", Path: "/dev/sde", Role: "data", SizeBytes: 2 << 30}}, testDisks...)
	vols := []Volume{healthy}

	if d, err := ValidateExpand(healthy, "sdd", all, vols); err != nil || d != nil {
		t.Errorf("the hot spare should be usable for expansion: %v %v", d, err)
	}
	if d, err := ValidateExpand(healthy, "sde", all, vols); err != nil || d == nil || d.Name != "sde" {
		t.Errorf("a free disk should be usable: %v %v", d, err)
	}
	if _, err := ValidateExpand(healthy, "sda", all, vols); err == nil {
		t.Error("an active member can't be used to expand")
	}
	for _, bad := range []Volume{
		{Level: "raid1", Status: "healthy"},
		{Level: "raid5", Status: "degraded"},
		{Level: "raid5", Status: "syncing"},
	} {
		if _, err := ValidateExpand(bad, "sde", all, vols); err == nil {
			t.Errorf("expanding %+v must be refused", bad)
		}
	}
}

func TestExpandWithSpare(t *testing.T) {
	s, rec := newTestService(t)
	// The fake array is rebuilding; make it idle so the reshape wait ends.
	md := filepath.Join(s.Sys, "block", "md127", "md")
	os.WriteFile(filepath.Join(md, "sync_action"), []byte("idle\n"), 0o644)
	os.WriteFile(filepath.Join(md, "degraded"), []byte("0\n"), 0o644)

	v := Volume{Name: "media", Device: "/dev/md127", Level: "raid5", RaidDisks: 3, Filesystem: "btrfs", Mountpoint: "/data/media"}
	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }
	if err := s.Expand(context.Background(), rec.run, logf, v, nil); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(rec.cmds, "\n")
	for _, want := range []string{
		"mdadm --grow /dev/md127 --raid-devices=4",
		"btrfs filesystem resize max /data/media",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "--add") || strings.Contains(all, "parted") {
		t.Errorf("expanding with the existing spare must not partition or add a disk:\n%s", all)
	}
}
