package backups

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
)

// Copy USB content upon connection (like the original ReadyNAS): plug a
// camera card or a stick, its files land in a share, in a new folder named
// after the date, so nothing is ever overwritten.

func ValidateUSBCopy(c *store.USBCopy, st *store.State) error {
	if !c.Enabled {
		return nil
	}
	if st.FindShare(c.Share) == nil {
		return fmt.Errorf("share %q not found", c.Share)
	}
	c.Path = strings.Trim(strings.TrimSpace(c.Path), "/")
	if strings.Contains("/"+c.Path+"/", "/../") || strings.ContainsAny(c.Path, "\n\r") {
		return errors.New("the folder can't contain '..'")
	}
	if c.Owner != "" {
		if u := st.FindUser(c.Owner); u == nil || u.Admin {
			return fmt.Errorf("unknown user %q", c.Owner)
		}
	}
	return nil
}

func (s *Service) SaveUSBCopy(c store.USBCopy) error {
	return s.Store.Update(func(st *store.State) error {
		if err := ValidateUSBCopy(&c, st); err != nil {
			return err
		}
		st.Settings.USBCopy = c
		return nil
	})
}

// RunUSBWatcher looks for newly plugged USB disks every 5 seconds.
func (s *Service) RunUSBWatcher(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		s.usbTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) usbTick(ctx context.Context) {
	disks, err := s.USBDisks(ctx)
	if err != nil {
		return
	}
	plugged := map[string]USBVolume{}
	for _, d := range disks {
		for _, v := range d.Volumes {
			plugged[v.UUID] = v
		}
	}
	st := s.Store.Read()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usbSeen == nil { // first look: what's already plugged in at startup isn't "new"
		s.usbSeen = plugged
		return
	}
	cfg := st.Settings.USBCopy
	for uuid := range plugged {
		if _, known := s.usbSeen[uuid]; known || !cfg.Enabled {
			continue
		}
		if usedByBackup(&st, uuid) {
			// Never copy a backup disk back into a share.
			log.Printf("USB disk %s is a backup target: not copied", uuid)
			continue
		}
		s.usbPending[uuid] = s.Now()
	}
	s.usbSeen = plugged // unplugged disks are forgotten: plugging them again copies again

	for uuid, since := range s.usbPending {
		v, ok := plugged[uuid]
		if !ok {
			delete(s.usbPending, uuid) // unplugged before we could copy
			continue
		}
		_, err := s.startUSBCopy(v, cfg, &st)
		switch {
		case err == nil:
			delete(s.usbPending, uuid)
		case errors.Is(err, jobs.ErrBusy) && s.Now().Sub(since) < 10*time.Minute:
			// retry at the next tick
		default:
			delete(s.usbPending, uuid)
			if s.Notify != nil {
				s.Notify("backup_failed", "USB copy not started", err.Error())
			}
		}
	}
}

// ForgetNewUSB marks the USB filesystems plugged right now as already seen,
// e.g. after formatting a disk (its new filesystem isn't a disk to copy).
func (s *Service) ForgetNewUSB(ctx context.Context) {
	disks, err := s.USBDisks(ctx)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usbSeen == nil {
		s.usbSeen = map[string]USBVolume{}
	}
	for _, d := range disks {
		for _, v := range d.Volumes {
			s.usbSeen[v.UUID] = v
			delete(s.usbPending, v.UUID)
		}
	}
}

func usedByBackup(st *store.State, uuid string) bool {
	for _, b := range st.Backups {
		if b.Target.Kind == "usb" && b.Target.USBUUID == uuid {
			return true
		}
	}
	return false
}

func (s *Service) startUSBCopy(v USBVolume, cfg store.USBCopy, st *store.State) (jobs.Snapshot, error) {
	share := st.FindShare(cfg.Share)
	if share == nil {
		return jobs.Snapshot{}, fmt.Errorf("share %s no longer exists", cfg.Share)
	}
	name := orDefault(v.Label, v.Disk)
	folder := s.Now().Format("2006-01-02_150405") + "_" + safeName(name)
	parent := filepath.Join(share.Path, cfg.Path)
	dest := filepath.Join(parent, folder)

	return s.Jobs.Start("Copy USB disk "+name+" to "+cfg.Share, 6*time.Hour, func(ctx context.Context, j *jobs.Job) error {
		mnt := filepath.Join(s.MountBase, "copy-"+v.UUID)
		if err := os.MkdirAll(mnt, 0o755); err != nil {
			return err
		}
		// Read-only: whatever happens, the USB disk is never modified.
		if _, err := j.Run(ctx, "mount", "-o", "ro", v.Device, mnt); err != nil {
			return err
		}
		defer func() {
			j.Run(context.Background(), "umount", mnt)
			os.Remove(mnt)
		}()
		// The parent folder must exist with the shares' group and setgid.
		if _, err := j.Run(ctx, "install", "-d", "-m", "2775", "-g", "nasusers", parent); err != nil {
			return err
		}
		args := []string{"--human-readable", "--info=stats2", "--recursive", "--times", "--modify-window=2",
			"--chmod=D2775,F664"}
		if cfg.Owner != "" {
			args = append(args, "--chown="+cfg.Owner+":nasusers")
		} else {
			args = append(args, "--chown=root:nasusers")
		}
		out, err := j.Run(ctx, "rsync", append(args, mnt+"/", dest+"/")...)
		if err != nil {
			return err
		}
		if s.Notify != nil {
			s.Notify("usb_copy", "USB disk "+name+" copied",
				fmt.Sprintf("Copied to %s/%s: %s. You can unplug the disk.", cfg.Share, filepath.Join(cfg.Path, folder), summarize(string(out))))
		}
		return nil
	})
}

// safeName keeps a label usable as a folder name.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '_'
		}
		return r
	}, s)
}
