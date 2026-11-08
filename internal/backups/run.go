package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/run"
	"readynas/internal/store"
)

// Linux filesystems keep owners, permissions and symlinks; FAT/exFAT/NTFS
// USB disks don't, so rsync must not try (it would fail on every file).
var linuxFS = []string{"ext4", "ext3", "ext2", "btrfs", "xfs"}

// Start runs a backup job now, as a background job (it can take hours the
// first time). trigger is "manual" or "schedule", for the log.
func (s *Service) Start(id int, trigger string) (jobs.Snapshot, error) {
	st := s.Store.Read()
	b := st.FindBackup(id)
	if b == nil {
		return jobs.Snapshot{}, fmt.Errorf("backup job %d not found", id)
	}
	job := *b
	share := st.FindShare(job.Share)
	if share == nil {
		return jobs.Snapshot{}, fmt.Errorf("share %s no longer exists", job.Share)
	}
	sharePath := share.Path

	title := fmt.Sprintf("Backup %q (%s)", job.Name, trigger)
	return s.Jobs.Start(title, 24*time.Hour, func(ctx context.Context, j *jobs.Job) error {
		started := s.Now()
		summary, err := s.execute(ctx, j, job, sharePath)
		run := store.BackupRun{Status: "done", Started: started, Ended: s.Now(), Summary: summary}
		if err != nil {
			run.Status, run.Summary = "failed", err.Error()
			if s.Notify != nil {
				s.Notify("backup_failed", "Backup \""+job.Name+"\" failed", err.Error())
			}
		}
		s.Store.Update(func(st *store.State) error {
			if b := st.FindBackup(job.ID); b != nil {
				b.LastRun = run
			}
			return nil
		})
		return err
	})
}

func (s *Service) execute(ctx context.Context, j *jobs.Job, b store.BackupJob, sharePath string) (string, error) {
	if _, err := os.Stat(sharePath); err != nil {
		return "", fmt.Errorf("share folder %s is not available (volume not mounted?)", sharePath)
	}
	if b.Target.Kind == "s3" {
		return s.executeRclone(ctx, j, b, sharePath)
	}
	remote, extra, cleanup, err := s.prepareTarget(ctx, j, b)
	defer cleanup()
	if err != nil {
		return "", err
	}

	args := []string{"--human-readable", "--info=stats2", "--protect-args"}
	args = append(args, extra...)
	if b.Mirror {
		args = append(args, "--delete")
	}
	args = append(args, "--exclude=.recycle/")
	for _, e := range b.Excludes {
		args = append(args, "--exclude="+e)
	}
	local := strings.TrimSuffix(sharePath, "/") + "/" // trailing "/": copy the folder's contents
	if b.Direction == "push" {
		args = append(args, local, remote)
		j.Logf("Copying share %s to %s", b.Share, describe(b.Target))
	} else {
		// Files arriving in a share must belong to the NAS users' group,
		// whatever their owner was on the source.
		args = append(args, "--no-owner", "--no-group", "--chmod=D2775,F664", remote, local)
		j.Logf("Copying %s into share %s", describe(b.Target), b.Share)
	}

	out, err := j.Run(ctx, "rsync", args...)
	summary := summarize(string(out))
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 24 {
		// 24 = some files vanished during the copy (deleted meanwhile): normal on a live share.
		return summary + " (some files changed during the backup)", nil
	}
	return summary, err
}

// prepareTarget returns the rsync source/destination for the target, extra
// rsync options, and a cleanup function (unmount, delete password file).
func (s *Service) prepareTarget(ctx context.Context, j *jobs.Job, b store.BackupJob) (string, []string, func(), error) {
	t := b.Target
	noop := func() {}
	switch t.Kind {
	case "usb":
		fs, err := s.mountUSB(ctx, j.Run, t.USBUUID)
		if err != nil {
			return "", nil, noop, err
		}
		dir := s.mountDir(t.USBUUID)
		cleanup := func() {
			j.Run(context.Background(), "umount", dir) // unmount even if ctx expired: the disk can then be unplugged
			os.Remove(dir)
		}
		target := filepath.Join(dir, t.Path)
		if err := os.MkdirAll(target, 0o755); err != nil {
			return "", nil, cleanup, err
		}
		opts := []string{"--archive"}
		if !contains(linuxFS, fs) {
			// FAT/exFAT/NTFS: files and dates only. FAT stores times with a
			// 2 s precision, hence the modify window.
			opts = []string{"--recursive", "--times", "--modify-window=2"}
			j.Logf("%s disk: owners, permissions and symlinks are not kept", fs)
		}
		return target + "/", opts, cleanup, nil

	case "ssh":
		return fmt.Sprintf("%s@%s:%s/", t.User, t.Host, strings.TrimSuffix(t.Path, "/")),
			[]string{"--archive", "-e", s.sshCommand(t.Port)}, noop, nil

	case "rsync":
		opts := []string{"--archive", "--port", fmt.Sprint(t.Port)}
		cleanup := noop
		if t.Password != "" {
			// The password goes in a 0600 file, never on the command line.
			f, err := os.CreateTemp(s.DataDir, ".rsync-pass-*")
			if err != nil {
				return "", nil, noop, err
			}
			f.WriteString(t.Password + "\n")
			f.Close()
			cleanup = func() { os.Remove(f.Name()) }
			opts = append(opts, "--password-file="+f.Name())
		}
		return rsyncURL(t), opts, cleanup, nil
	}
	return "", nil, noop, fmt.Errorf("unknown target %q", t.Kind)
}

func (s *Service) sshCommand(port int) string {
	// BatchMode: fail instead of asking for a password nobody can type.
	// accept-new: trust a host the first time, refuse if its key changes.
	return fmt.Sprintf("ssh -i %s -p %d -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=%s",
		s.keyPath(), port, s.knownHostsPath())
}

func rsyncURL(t store.BackupTarget) string {
	user := ""
	if t.User != "" {
		user = t.User + "@"
	}
	u := fmt.Sprintf("rsync://%s%s/%s/", user, t.Host, t.Module)
	if t.Path != "" {
		u += t.Path + "/"
	}
	return u
}

func describe(t store.BackupTarget) string {
	switch t.Kind {
	case "usb":
		return "USB disk " + t.USBUUID + ":/" + t.Path
	case "ssh":
		return fmt.Sprintf("%s@%s:%s", t.User, t.Host, t.Path)
	case "s3":
		return fmt.Sprintf("s3://%s/%s (%s %s)", t.Bucket, t.Path, t.Provider, t.Region)
	default:
		return rsyncURL(t)
	}
}

var statRe = regexp.MustCompile(`(?m)^(Number of regular files transferred|Total transferred file size|Number of deleted files): ([^\n]+)`)

// summarize turns rsync's --info=stats2 output into one line, in a fixed
// order whatever order rsync prints them in.
func summarize(out string) string {
	found := map[string]string{}
	for _, m := range statRe.FindAllStringSubmatch(out, -1) {
		found[m[1]] = strings.TrimSpace(m[2])
	}
	var parts []string
	if v, ok := found["Number of regular files transferred"]; ok {
		parts = append(parts, v+map[bool]string{true: " file copied", false: " files copied"}[v == "1"])
	}
	if v, ok := found["Total transferred file size"]; ok {
		parts = append(parts, strings.TrimSuffix(v, " bytes")+" transferred")
	}
	if v := found["Number of deleted files"]; v != "" && v != "0" {
		parts = append(parts, v+" deleted")
	}
	return strings.Join(parts, ", ")
}

// ------------------------------------------------------------ USB disks

type USBVolume struct {
	UUID   string `json:"uuid"`
	Device string `json:"device"` // /dev/sde1
	Disk   string `json:"disk"`   // sde
	Label  string `json:"label"`
	FSType string `json:"fstype"`
	Size   int64  `json:"sizeBytes"`
	Model  string `json:"model"`
}

type USBDisk struct {
	Disk    string      `json:"disk"`
	Model   string      `json:"model"`
	Size    int64       `json:"sizeBytes"`
	Volumes []USBVolume `json:"volumes"` // filesystems usable for backups
}

// USBDisks lists plugged USB disks and the filesystems on them.
func (s *Service) USBDisks(ctx context.Context) ([]USBDisk, error) {
	out, err := s.Run(ctx, "lsblk", "--json", "--bytes", "--tree", "-o", "NAME,PATH,SIZE,MODEL,TRAN,TYPE,FSTYPE,UUID,LABEL")
	if err != nil {
		return nil, err
	}
	type dev struct {
		Name     string  `json:"name"`
		Path     string  `json:"path"`
		Size     int64   `json:"size"`
		Model    *string `json:"model"`
		Tran     *string `json:"tran"`
		Type     string  `json:"type"`
		FSType   *string `json:"fstype"`
		UUID     *string `json:"uuid"`
		Label    *string `json:"label"`
		Children []dev   `json:"children"`
	}
	var doc struct {
		Blockdevices []dev `json:"blockdevices"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, err
	}
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return strings.TrimSpace(*p)
	}
	disks := []USBDisk{}
	for _, d := range doc.Blockdevices {
		if d.Type != "disk" || str(d.Tran) != "usb" {
			continue
		}
		disk := USBDisk{Disk: d.Name, Model: str(d.Model), Size: d.Size, Volumes: []USBVolume{}}
		// A filesystem can sit on the whole disk or on its partitions.
		for _, p := range append([]dev{d}, d.Children...) {
			if str(p.UUID) != "" && str(p.FSType) != "" && !strings.HasSuffix(str(p.FSType), "_member") {
				disk.Volumes = append(disk.Volumes, USBVolume{
					UUID: str(p.UUID), Device: p.Path, Disk: d.Name, Label: str(p.Label),
					FSType: str(p.FSType), Size: p.Size, Model: disk.Model,
				})
			}
		}
		disks = append(disks, disk)
	}
	return disks, nil
}

func (s *Service) mountUSB(ctx context.Context, r run.Func, uuid string) (string, error) {
	disks, err := s.USBDisks(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range disks {
		for _, v := range d.Volumes {
			if v.UUID != uuid {
				continue
			}
			dir := s.mountDir(uuid)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return "", err
			}
			if _, err := r(ctx, "mount", v.Device, dir); err != nil {
				return "", err
			}
			return v.FSType, nil
		}
	}
	return "", errors.New("the USB disk for this backup is not plugged in")
}

// FormatUSB erases a USB disk and creates one filesystem on it: ext4
// (Linux only, keeps permissions) or exFAT (readable by Windows and macOS).
func (s *Service) FormatUSB(ctx context.Context, r run.Func, disk, fstype, label string) error {
	dev := "/dev/" + disk
	cmds := [][]string{
		{"wipefs", "--all", dev},
		{"parted", "-s", dev, "--", "mklabel", "gpt", "mkpart", "backup", "1MiB", "100%"},
		{"udevadm", "settle"},
	}
	part := dev + "1"
	if strings.ContainsAny(disk[len(disk)-1:], "0123456789") {
		part = dev + "p1"
	}
	switch fstype {
	case "ext4":
		cmds = append(cmds, []string{"mkfs.ext4", "-F", "-q", "-L", label, part})
	case "exfat":
		cmds = append(cmds, []string{"mkfs.exfat", "-L", label, part})
	default:
		return fmt.Errorf("unsupported filesystem %q", fstype)
	}
	for _, c := range cmds {
		if _, err := r(ctx, c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------ connection test

// Test checks that a target is reachable and that the NAS can log in,
// without copying anything. It answers within ~20 s.
func (s *Service) Test(ctx context.Context, t store.BackupTarget, stored store.BackupTarget) (string, error) {
	if stored.Kind == t.Kind {
		keepSecrets(&t, stored)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch t.Kind {
	case "usb":
		disks, err := s.USBDisks(ctx)
		if err != nil {
			return "", err
		}
		for _, d := range disks {
			for _, v := range d.Volumes {
				if v.UUID == t.USBUUID {
					return fmt.Sprintf("USB disk %s (%s) is plugged in", orDefault(v.Label, v.Device), v.FSType), nil
				}
			}
		}
		return "", errors.New("this USB disk is not plugged in")
	case "ssh":
		if _, err := s.PublicKey(ctx); err != nil {
			return "", err
		}
		// ssh joins its arguments into a command for the remote shell, so the
		// path must be shell-quoted. mkdir -p also checks we can write there.
		args := append(strings.Fields(s.sshCommand(t.Port))[1:], fmt.Sprintf("%s@%s", t.User, t.Host), "mkdir -p -- "+shellQuote(t.Path))
		if _, err := s.Run(ctx, "ssh", args...); err != nil {
			return "", fmt.Errorf("SSH login failed. Is the NAS key in %s's authorized_keys? %w", t.User, err)
		}
		return fmt.Sprintf("Logged in to %s as %s; folder %s is ready", t.Host, t.User, t.Path), nil
	case "s3":
		if err := validateS3(&t); err != nil {
			return "", err
		}
		env, remote, err := s.rcloneRemote(ctx, t, false)
		if err != nil {
			return "", err
		}
		out, err := s.RunEnv(ctx, env, "rclone", "--config", "", "lsf", "--max-depth", "1", remote)
		if err != nil {
			return "", fmt.Errorf("cannot reach the bucket: %w", err)
		}
		n := len(strings.Fields(string(out)))
		return fmt.Sprintf("Connected to %s/%s (%d items at the top)%s", t.Bucket, t.Path, n,
			map[bool]string{true: ", encrypted", false: ""}[t.EncryptPassword != ""]), nil
	case "rsync":
		opts := []string{"--list-only", "--port", fmt.Sprint(t.Port)}
		if t.Password != "" {
			f, err := os.CreateTemp(s.DataDir, ".rsync-pass-*")
			if err != nil {
				return "", err
			}
			defer os.Remove(f.Name())
			f.WriteString(t.Password + "\n")
			f.Close()
			opts = append(opts, "--password-file="+f.Name())
		}
		out, err := s.Run(ctx, "rsync", append(opts, rsyncURL(t))...)
		if err != nil {
			return "", err
		}
		n := len(strings.Split(strings.TrimSpace(string(out)), "\n"))
		return fmt.Sprintf("Connected to rsync://%s/%s (%d entries at the top level)", t.Host, t.Module, n), nil
	}
	return "", fmt.Errorf("unknown target %q", t.Kind)
}

// shellQuote wraps s in single quotes for a POSIX shell ('it'"'"'s').
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
