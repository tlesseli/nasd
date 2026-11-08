package backups

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
)

const usbLsblk = `{"blockdevices":[
 {"name":"sda","path":"/dev/sda","size":100,"model":"DISK","tran":"sata","type":"disk","fstype":null,"uuid":null,"label":null},
 {"name":"sde","path":"/dev/sde","size":1000,"model":"Stick","tran":"usb","type":"disk","fstype":null,"uuid":null,"label":null,
  "children":[{"name":"sde1","path":"/dev/sde1","size":999,"model":null,"tran":null,"type":"part","fstype":"exfat","uuid":"ABCD-1234","label":"BACKUP"}]},
 {"name":"sdf","path":"/dev/sdf","size":125,"model":"USB DISK","tran":"usb","type":"disk","fstype":null,"uuid":null,"label":null,
  "children":[{"name":"sdf1","path":"/dev/sdf1","size":124,"type":"part","fstype":"vfat","uuid":"4C74-AA3E","label":null,"mountpoints":["/boot/flash"]}]},
 {"name":"sdg","path":"/dev/sdg","size":16000,"model":"SSD","tran":"usb","type":"disk","fstype":null,"uuid":null,"label":null,
  "children":[{"name":"sdg1","path":"/dev/sdg1","size":15000,"type":"part","fstype":"ext4","uuid":"1111","label":null,"mountpoints":["/"]},
   {"name":"sdg15","path":"/dev/sdg15","size":124,"type":"part","fstype":"vfat","uuid":"2222","label":null,"mountpoints":["/boot/efi"]}]}]}`

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
		return []byte(usbLsblk), nil
	case "rsync":
		return []byte("Number of files: 10\nNumber of regular files transferred: 3\nNumber of deleted files: 1\nTotal transferred file size: 1.50K bytes\n"), nil
	}
	return nil, nil
}

func (f *fake) all() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.cmds, "\n")
}

func newTestService(t *testing.T) (*Service, *fake, string) {
	t.Helper()
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "state.json"))
	share := filepath.Join(dir, "data", "Photos")
	os.MkdirAll(share, 0o755)
	st.Update(func(s *store.State) error {
		s.Shares = []store.Share{{Name: "Photos", Path: share}}
		return nil
	})
	f := &fake{}
	s := NewService(st, jobs.NewManager(f.run), dir)
	s.Run = f.run
	s.MountBase = filepath.Join(dir, "usb")
	return s, f, share
}

func TestValidate(t *testing.T) {
	st := store.State{Shares: []store.Share{{Name: "Photos"}}}
	ok := store.BackupJob{Name: "Nightly", Direction: "push", Share: "Photos",
		Target: store.BackupTarget{Kind: "ssh", Host: "192.168.1.10", User: "backup", Path: "/srv/backup"}}
	if err := Validate(&ok, &st); err != nil || ok.Target.Port != 22 {
		t.Fatalf("valid job refused or port not defaulted: %v %+v", err, ok.Target)
	}
	usb := store.BackupJob{Name: "USB", Direction: "push", Share: "Photos", Target: store.BackupTarget{Kind: "usb", USBUUID: "ABCD-1234"}}
	if err := Validate(&usb, &st); err != nil || usb.Target.Path != "nas-backup/Photos" {
		t.Fatalf("usb default path: %v %q", err, usb.Target.Path)
	}

	bad := map[string]func(j *store.BackupJob){
		"no name":        func(j *store.BackupJob) { j.Name = " " },
		"direction":      func(j *store.BackupJob) { j.Direction = "both" },
		"unknown share":  func(j *store.BackupJob) { j.Share = "Nope" },
		"relative path":  func(j *store.BackupJob) { j.Target.Path = "backup" },
		"dot dot":        func(j *store.BackupJob) { j.Target.Path = "/srv/../etc" },
		"host injection": func(j *store.BackupJob) { j.Target.Host = "-oProxyCommand=evil" },
		"user injection": func(j *store.BackupJob) { j.Target.User = "a b" },
		"schedule":       func(j *store.BackupJob) { j.Schedule = store.Schedule{Kind: "daily", Hour: 24} },
		"kind":           func(j *store.BackupJob) { j.Target.Kind = "ftp" },
	}
	for label, mutate := range bad {
		j := ok
		mutate(&j)
		if err := Validate(&j, &st); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func runJob(t *testing.T, s *Service, b store.BackupJob) store.BackupJob {
	t.Helper()
	id, err := s.Save(b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(id, "manual"); err != nil {
		t.Fatal(err)
	}
	s.Jobs.Wait()
	st := s.Store.Read()
	return *st.FindBackup(id)
}

func TestPushToUSBExfat(t *testing.T) {
	s, f, share := newTestService(t)
	got := runJob(t, s, store.BackupJob{Name: "USB", Direction: "push", Share: "Photos", Mirror: true,
		Excludes: []string{"*.tmp"}, Target: store.BackupTarget{Kind: "usb", USBUUID: "ABCD-1234"}})
	all := f.all()
	mnt := filepath.Join(s.MountBase, "ABCD-1234")
	for _, want := range []string{
		"mount /dev/sde1 " + mnt,
		"rsync --human-readable --info=stats2 --protect-args --recursive --times --modify-window=2 --delete --exclude=.recycle/ --exclude=*.tmp " +
			share + "/ " + mnt + "/nas-backup/Photos/",
		"umount " + mnt,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if got.LastRun.Status != "done" || got.LastRun.Summary != "3 files copied, 1.50K transferred, 1 deleted" {
		t.Errorf("last run: %+v", got.LastRun)
	}
}

func TestPullFromRsyncServer(t *testing.T) {
	s, f, share := newTestService(t)
	runJob(t, s, store.BackupJob{Name: "Old NAS", Direction: "pull", Share: "Photos",
		Target: store.BackupTarget{Kind: "rsync", Host: "192.168.1.5", User: "admin", Password: "s3cret", Module: "ELC"}})
	all := f.all()
	if strings.Contains(all, "s3cret") {
		t.Error("the password must never appear in command arguments")
	}
	for _, want := range []string{"--port 873 --password-file=", "--no-owner --no-group --chmod=D2775,F664 rsync://admin@192.168.1.5/ELC/ " + share + "/"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(s.DataDir, ".rsync-pass-*"))
	if len(leftovers) > 0 {
		t.Errorf("password file not removed: %v", leftovers)
	}
	if list := s.List(); list[0].Target.Password != "" {
		t.Error("List must not return passwords")
	}
	// Saving without a password keeps the stored one.
	b := s.List()[0]
	s.Save(b)
	st := s.Store.Read()
	if st.FindBackup(b.ID).Target.Password != "s3cret" {
		t.Error("an empty password on update must keep the stored one")
	}
}

func TestPushOverSSH(t *testing.T) {
	s, f, _ := newTestService(t)
	runJob(t, s, store.BackupJob{Name: "Offsite", Direction: "push", Share: "Photos",
		Target: store.BackupTarget{Kind: "ssh", Host: "backup.lan", User: "nas", Path: "/srv/My Backups", Port: 2222}})
	all := f.all()
	if !strings.Contains(all, "-p 2222 -o BatchMode=yes") || !strings.Contains(all, "nas@backup.lan:/srv/My Backups/") {
		t.Errorf("ssh rsync command:\n%s", all)
	}
}

func TestDue(t *testing.T) {
	mon0300 := time.Date(2026, 11, 9, 3, 0, 0, 0, time.Local) // a Monday
	cases := []struct {
		s    store.Schedule
		t    time.Time
		want bool
	}{
		{store.Schedule{Kind: "daily", Hour: 3}, mon0300, true},
		{store.Schedule{Kind: "daily", Hour: 4}, mon0300, false},
		{store.Schedule{Kind: "weekly", Weekday: 1, Hour: 3}, mon0300, true},
		{store.Schedule{Kind: "weekly", Weekday: 2, Hour: 3}, mon0300, false},
		{store.Schedule{Kind: "hourly", Minute: 0}, mon0300.Add(5 * time.Hour), true},
		{store.Schedule{Kind: "", Hour: 3}, mon0300, false},
	}
	for _, c := range cases {
		if got := c.s.Due(c.t); got != c.want {
			t.Errorf("due(%+v, %s) = %v", c.s, c.t, got)
		}
	}
}

func TestSchedulerWaitsWhenBusy(t *testing.T) {
	s, _, _ := newTestService(t)
	now := time.Date(2026, 11, 9, 3, 0, 10, 0, time.Local)
	s.Now = func() time.Time { return now }
	id, _ := s.Save(store.BackupJob{Name: "Nightly", Direction: "push", Share: "Photos",
		Schedule: store.Schedule{Kind: "daily", Hour: 3}, Target: store.BackupTarget{Kind: "usb", USBUUID: "ABCD-1234"}})

	release := make(chan struct{})
	s.Jobs.Start("something long", time.Minute, func(context.Context, *jobs.Job) error { <-release; return nil })
	s.tick() // due, but busy: stays pending
	if len(s.pending) != 1 {
		t.Fatalf("job should be pending, got %v", s.pending)
	}
	close(release)
	s.Jobs.Wait()
	now = now.Add(40 * time.Second) // same minute: must not fire twice
	s.tick()
	s.Jobs.Wait()
	if len(s.pending) != 0 {
		t.Fatalf("job should have started: %v", s.pending)
	}
	st := s.Store.Read()
	if st.FindBackup(id).LastRun.Status != "done" {
		t.Errorf("last run: %+v", st.FindBackup(id).LastRun)
	}
	runs := 0
	for _, j := range s.Jobs.List() {
		if strings.Contains(j.Title, "Nightly") {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("backup ran %d times, want 1", runs)
	}
}

func TestUSBCopyOnConnection(t *testing.T) {
	s, f, share := newTestService(t)
	var notes []string
	s.Notify = func(k, subj, b string) { notes = append(notes, k+": "+subj) }
	ctx := context.Background()
	// The test lsblk always shows ABCD-1234 (label BACKUP). First look: it was
	// plugged before we started, so it's not copied.
	s.SaveUSBCopy(store.USBCopy{Enabled: true, Share: "Photos", Path: "Imports", Owner: ""})
	s.usbTick(ctx)
	s.Jobs.Wait()
	if strings.Contains(f.all(), "rsync") {
		t.Fatal("a disk present at startup must not be copied")
	}
	// Simulate unplug + replug.
	s.usbSeen = map[string]USBVolume{}
	s.usbTick(ctx)
	s.Jobs.Wait()
	all := f.all()
	for _, want := range []string{"mount -o ro /dev/sde1 ", "install -d -m 2775 -g nasusers " + share + "/Imports",
		"--chown=root:nasusers", share + "/Imports/"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "usb_copy: USB disk BACKUP copied") {
		t.Errorf("notes: %v", notes)
	}
	// A backup disk is never copied.
	f.cmds = nil
	s.Save(store.BackupJob{Name: "B", Direction: "push", Share: "Photos", Target: store.BackupTarget{Kind: "usb", USBUUID: "ABCD-1234"}})
	s.usbSeen = map[string]USBVolume{}
	s.usbTick(ctx)
	s.Jobs.Wait()
	if strings.Contains(f.all(), "rsync") {
		t.Error("a USB disk used by a backup job must not be copied into a share")
	}
	if err := s.SaveUSBCopy(store.USBCopy{Enabled: true, Share: "Photos", Path: "../etc"}); err == nil {
		t.Error("'..' must be refused")
	}
}

// On the ReadyNAS the system disk and the internal boot flash are USB disks
// too: they must never be offered for backups or USB copy.
func TestUSBDisksSkipsSystemDisks(t *testing.T) {
	s, _, _ := newTestService(t)
	list, err := s.USBDisks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Disk != "sde" {
		t.Fatalf("want only sde, got %+v", list)
	}
}
