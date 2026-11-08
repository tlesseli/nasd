package volumes

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	"readynas/internal/run"
)

// Per-user disk quotas (ext4 only: btrfs has no per-user quotas, only
// per-subvolume ones).
//
// ext4 always tracks usage per user when the filesystem has the "quota"
// feature; limits are enforced when it's mounted with the usrquota option.

// EnableQuotas adds quota support to an existing ext4 volume. The feature
// can only be added while unmounted, so the volume is briefly unavailable.
func (s *Service) EnableQuotas(ctx context.Context, r run.Func, v Volume) error {
	if v.Filesystem != "ext4" {
		return errors.New("per-user quotas need an ext4 volume (btrfs doesn't support them)")
	}
	if v.Mountpoint == "" || v.UUID == "" {
		return errors.New("the volume must be mounted")
	}
	mnt := filepath.Join(s.MountRoot, v.Name)
	unit, err := mountUnit(ctx, r, mnt)
	if err != nil {
		return err
	}
	if _, err := r(ctx, "systemctl", "stop", unit); err != nil {
		return errors.New("cannot unmount the volume: close the files open on it (computers using its shares) and try again")
	}
	// Whatever happens next, mount it back.
	defer r(context.Background(), "systemctl", "start", unit)
	if _, err := r(ctx, "tune2fs", "-O", "quota", "-Q", "usrquota", v.Device); err != nil {
		return err
	}
	if err := s.setFstab(v.Name, fstabLine(v.UUID, mnt, "ext4")); err != nil {
		return err
	}
	_, err = r(ctx, "systemctl", "daemon-reload")
	return err
}

type QuotaUse struct {
	User    string `json:"user"`
	UsedKB  int64  `json:"usedKB"`
	LimitKB int64  `json:"limitKB"` // 0 = unlimited
}

// QuotaUsage reports space used per user on a volume with quotas.
func (s *Service) QuotaUsage(ctx context.Context, v Volume) ([]QuotaUse, error) {
	if !v.Quotas {
		return nil, errors.New("quotas are not enabled on " + v.Name)
	}
	out, err := s.Run(ctx, "repquota", "-u", "-O", "csv", v.Mountpoint)
	if err != nil {
		return nil, err
	}
	return parseRepquota(string(out)), nil
}

// parseRepquota reads `repquota -O csv`:
// User,BlockStatus,FileStatus,BlockUsed,BlockSoftLimit,BlockHardLimit,...
func parseRepquota(out string) []QuotaUse {
	var list []QuotaUse
	for i, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, ",")
		if i == 0 || len(f) < 6 {
			continue
		}
		used, _ := strconv.ParseInt(f[3], 10, 64)
		hard, _ := strconv.ParseInt(f[5], 10, 64)
		list = append(list, QuotaUse{User: f[0], UsedKB: used, LimitKB: hard})
	}
	return list
}
