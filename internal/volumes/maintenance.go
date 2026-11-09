package volumes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"readynas/internal/run"
)

// StartCheck asks the kernel to read the whole array and compare every
// block with its mirror or parity ("scrubbing"). action is "check" (count
// mismatches only) or "repair" (rewrite parity where it doesn't match).
//
// It returns at once: the kernel runs the check in the background and the
// volume stays usable. It reports progress like a resync, and the result
// lands in mismatch_cnt (read by the monitor when the check ends).
//
// Why scrub? A sector can go bad silently and stay unnoticed for months,
// until a disk fails and the rebuild needs that exact sector. Reading
// everything regularly finds those sectors while redundancy still exists.
func (s *Service) StartCheck(v Volume, action string) error {
	if action != "check" && action != "repair" {
		return errors.New("action must be check or repair")
	}
	if v.Level == "raid0" {
		return errors.New("RAID 0 has no redundancy to check against")
	}
	if v.Status != "healthy" {
		return fmt.Errorf("volume is %s: wait until it is healthy", v.Status)
	}
	path := filepath.Join(s.Sys, "block", filepath.Base(v.Device), "md", "sync_action")
	return os.WriteFile(path, []byte(action), 0o644)
}

// CheckFilesystem verifies the filesystem itself (not the RAID):
//   - btrfs checksums every block and can verify them online: "btrfs scrub",
//   - ext4 can only be checked unmounted. We make the next boot check it:
//     a mount count above the maximum forces e2fsck when systemd mounts it.
//     Returns true when the check is postponed to the next boot.
func (s *Service) CheckFilesystem(ctx context.Context, r run.Func, v Volume) (bool, error) {
	switch v.Filesystem {
	case "btrfs":
		if v.Mountpoint == "" {
			return false, errors.New("the volume must be mounted")
		}
		// -B: wait for the end, so the job log shows the result.
		_, err := r(ctx, "btrfs", "scrub", "start", "-B", v.Mountpoint)
		return false, err
	case "ext4":
		_, err := r(ctx, "tune2fs", "-c", "1", "-C", "2", v.FSDevice())
		return true, err
	}
	return false, fmt.Errorf("don't know how to check %q", v.Filesystem)
}

// ResetBootCheck restores the normal mount count after a boot-time check.
func (s *Service) ResetBootCheck(ctx context.Context, r run.Func, v Volume) error {
	_, err := r(ctx, "tune2fs", "-c", "-1", v.FSDevice())
	return err
}
