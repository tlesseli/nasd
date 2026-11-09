package volumes

// Adopting a volume made by another system (RAIDiator, another NAS, mdadm by
// hand): nasd takes it over without touching the files on it.
//
//	RAIDiator:  4 disks ─ partition 3 ─ RAID 5 "00223FAA3674:2" ─ LVM c/c ─ ext4
//	after:      the same, with the array named "data", mounted on /data/data
//
// Steps, all visible in the job log:
//  1. rename the array (stop it, assemble it again under the new name): the
//     name becomes /dev/md/<name>, the mount point and the fstab marker,
//  2. check the filesystem (e2fsck -p: quick when it's clean),
//  3. mount it the same way Create does (fstab by UUID + systemd).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"readynas/internal/run"
)

// ValidateAdopt: an unmounted volume nasd doesn't manage yet, with a
// filesystem nasd knows, and a free, valid name.
func ValidateAdopt(v Volume, name string, vols []Volume) error {
	switch {
	case v.Managed:
		return errors.New("this volume is already in use")
	case v.Mountpoint != "":
		return fmt.Errorf("%s is mounted on %s: unmount it first", v.Name, v.Mountpoint)
	case v.Status == "failed" || v.Status == "inactive":
		return errors.New("the array isn't running: it can't be adopted in this state")
	case v.Filesystem == "LVM2_member":
		return errors.New("LVM with several (or inactive) logical volumes: not supported")
	case !slices.Contains(Filesystems, v.Filesystem):
		return fmt.Errorf("no usable filesystem found on this volume (%q)", v.Filesystem)
	case !nameRe.MatchString(name):
		return errors.New("name: lowercase letters, digits, - and _, starting with a letter, up to 16 characters")
	}
	for _, o := range vols {
		if o.Name == name && o.Device != v.Device {
			return fmt.Errorf("a volume named %s already exists", name)
		}
	}
	return nil
}

func (s *Service) Adopt(ctx context.Context, r run.Func, v Volume, name string) error {
	if v.Name != name {
		var err error
		if v, err = s.rename(ctx, r, v, name); err != nil {
			return err
		}
	}

	// e2fsck -p ("preen") fixes what is safe to fix and stops on anything
	// serious. Exit code 1 means "errors corrected": fine.
	if v.Filesystem == "ext4" {
		if _, err := r(ctx, "e2fsck", "-p", v.FSDevice()); err != nil && !strings.Contains(err.Error(), "exit status 1") {
			return fmt.Errorf("the filesystem check found problems it can't fix safely: %w", err)
		}
	}

	// Quotas are enforced only if the filesystem has ext4's quota feature;
	// RAIDiator used older quota files instead. "Enable quotas" adds it later.
	quotas := false
	if v.Filesystem == "ext4" {
		out, _ := r(ctx, "dumpe2fs", "-h", v.FSDevice())
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(l, "Filesystem features:") {
				quotas = slices.Contains(strings.Fields(strings.TrimPrefix(l, "Filesystem features:")), "quota")
			}
		}
	}

	mnt := filepath.Join(s.MountRoot, name)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	line := fstabLine(v.UUID, mnt, v.Filesystem)
	if !quotas {
		line = strings.Replace(line, ",usrquota", "", 1)
	}
	if err := s.setFstab(name, line); err != nil {
		return err
	}
	if _, err := r(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	unit, err := mountUnit(ctx, r, mnt)
	if err != nil {
		return err
	}
	if _, err := r(ctx, "systemctl", "start", unit); err != nil {
		return err
	}
	return s.saveMdadmConf(ctx, r)
}

// rename stops the array and assembles it again with a new name. The data
// isn't touched: only the name in each member's RAID superblock changes.
func (s *Service) rename(ctx context.Context, r run.Func, v Volume, name string) (Volume, error) {
	var members []string
	for _, m := range v.Members {
		members = append(members, m.Device)
	}
	if v.VG != "" {
		if _, err := r(ctx, "vgchange", "-an", v.VG); err != nil {
			return v, err
		}
	}
	if _, err := r(ctx, "mdadm", "--stop", v.Device); err != nil {
		return v, err
	}
	md := "/dev/md/" + name
	args := append([]string{"--assemble", md, "--run", "--update=name", "--name=" + name}, members...)
	if _, err := r(ctx, "mdadm", args...); err != nil {
		// Put it back as it was, so nothing is left stopped.
		r(ctx, "mdadm", append([]string{"--assemble", "--run", v.Device}, members...)...)
		if v.VG != "" {
			r(ctx, "vgchange", "-ay", v.VG)
		}
		return v, err
	}
	r(ctx, "udevadm", "settle")
	if v.VG != "" {
		if _, err := r(ctx, "vgchange", "-ay", v.VG); err != nil {
			return v, err
		}
		r(ctx, "udevadm", "settle")
	}
	dev, err := filepath.EvalSymlinks(md)
	if err != nil {
		return v, fmt.Errorf("array assembled but %s is missing: %w", md, err)
	}
	vols, err := s.List(ctx)
	if err != nil {
		return v, err
	}
	for _, o := range vols {
		if o.Device == dev {
			return o, nil
		}
	}
	return v, fmt.Errorf("array %s not found after renaming", dev)
}
