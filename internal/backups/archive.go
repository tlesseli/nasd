package backups

// Cloud archive with rclone: AWS S3 (Glacier Deep Archive), Scaleway Object
// Storage (Glacier), or any S3-compatible service.
//
// Archive storage classes are very cheap to store (~1 €/TB/month) but files
// are "frozen": reading them back needs a retrieval request first, which
// takes hours (up to 48 h for AWS Deep Archive), and deleting them early
// costs a minimum duration fee (90-180 days). Hence:
//   - archive jobs copy by default (never delete); mirror is possible but warned,
//   - restoring is two steps: "Request retrieval", then a pull job.
//
// Files can be encrypted before they leave the NAS (rclone "crypt"): names
// and contents are unreadable for the provider. Without the password the
// archive is unreadable for you too: keep it somewhere safe.
//
// The remote is described entirely through environment variables
// (RCLONE_CONFIG_<NAME>_<OPTION>): no config file with keys on disk, and no
// secret in the command line.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
)

var (
	bucketRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe    = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
	accessKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/=._-]{8,128}$`)
	classRe     = regexp.MustCompile(`^[A-Z_]{0,32}$`)
)

// StorageClasses per provider; the first one is the default.
var StorageClasses = map[string][]string{
	"aws":      {"DEEP_ARCHIVE", "GLACIER", "GLACIER_IR", "STANDARD_IA", "STANDARD"},
	"scaleway": {"GLACIER", "ONEZONE_IA", "STANDARD"},
}

func validateS3(t *store.BackupTarget) error {
	switch t.Provider {
	case "aws", "scaleway":
		t.Endpoint = ""
	case "other":
		u, err := url.Parse(t.Endpoint)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("endpoint must be a URL like https://s3.example.com")
		}
	default:
		return errors.New("choose a provider: aws, scaleway or other")
	}
	if !regionRe.MatchString(t.Region) {
		return errors.New("enter the region (e.g. eu-west-3 on AWS, fr-par on Scaleway)")
	}
	if !bucketRe.MatchString(t.Bucket) {
		return errors.New("bucket name: 3-63 lowercase letters, digits, '.' or '-'")
	}
	if !accessKeyRe.MatchString(t.AccessKey) {
		return errors.New("enter the access key")
	}
	if t.SecretKey == "" || strings.ContainsAny(t.SecretKey, "\n\r") {
		return errors.New("enter the secret key")
	}
	if classes, ok := StorageClasses[t.Provider]; ok {
		if t.StorageClass == "" {
			t.StorageClass = classes[0]
		}
		if !slices.Contains(classes, t.StorageClass) {
			return fmt.Errorf("storage class must be one of %v", classes)
		}
	} else if !classRe.MatchString(t.StorageClass) {
		return errors.New("invalid storage class")
	}
	if t.EncryptPassword != "" && (len(t.EncryptPassword) < 12 || strings.ContainsAny(t.EncryptPassword, "\n\r")) {
		return errors.New("the encryption password needs at least 12 characters")
	}
	if t.BandwidthKB < 0 || t.BandwidthKB > 10_000_000 {
		return errors.New("invalid bandwidth limit")
	}
	t.Path = strings.Trim(t.Path, "/")
	t.USBUUID, t.Host, t.User, t.Password, t.Module, t.Port = "", "", "", "", "", 0
	return nil
}

// isArchiveClass: storage classes whose files must be retrieved before reading.
func isArchiveClass(c string) bool { return c == "DEEP_ARCHIVE" || c == "GLACIER" }

// rcloneRemote returns the environment describing the remote, and the
// remote path to use ("secure:" when encrypted, else "archive:bucket/path").
// raw=true skips the encryption layer (for retrieval requests on the objects).
func (s *Service) rcloneRemote(ctx context.Context, t store.BackupTarget, raw bool) ([]string, string, error) {
	provider := map[string]string{"aws": "AWS", "scaleway": "Scaleway", "other": "Other"}[t.Provider]
	endpoint := t.Endpoint
	if t.Provider == "scaleway" {
		endpoint = "https://s3." + t.Region + ".scw.cloud"
	}
	env := []string{
		"RCLONE_CONFIG_ARCHIVE_TYPE=s3",
		"RCLONE_CONFIG_ARCHIVE_PROVIDER=" + provider,
		"RCLONE_CONFIG_ARCHIVE_ENV_AUTH=false",
		"RCLONE_CONFIG_ARCHIVE_ACCESS_KEY_ID=" + t.AccessKey,
		"RCLONE_CONFIG_ARCHIVE_SECRET_ACCESS_KEY=" + t.SecretKey,
		"RCLONE_CONFIG_ARCHIVE_REGION=" + t.Region,
	}
	if endpoint != "" {
		env = append(env, "RCLONE_CONFIG_ARCHIVE_ENDPOINT="+endpoint)
	}
	if t.StorageClass != "" {
		env = append(env, "RCLONE_CONFIG_ARCHIVE_STORAGE_CLASS="+t.StorageClass)
	}
	base := "archive:" + t.Bucket
	if t.Path != "" {
		base += "/" + t.Path
	}
	if t.EncryptPassword == "" || raw {
		return env, base, nil
	}
	// rclone wants the crypt password "obscured"; `rclone obscure -` reads
	// it from stdin, so it never shows up in a command line.
	out, err := s.RunInput(ctx, t.EncryptPassword+"\n", "rclone", "obscure", "-")
	if err != nil {
		return nil, "", err
	}
	env = append(env,
		"RCLONE_CONFIG_SECURE_TYPE=crypt",
		"RCLONE_CONFIG_SECURE_REMOTE="+base,
		"RCLONE_CONFIG_SECURE_PASSWORD="+strings.TrimSpace(string(out)),
		"RCLONE_CONFIG_SECURE_FILENAME_ENCRYPTION=standard",
		"RCLONE_CONFIG_SECURE_DIRECTORY_NAME_ENCRYPTION=true",
	)
	return env, "secure:", nil
}

func (s *Service) executeRclone(ctx context.Context, j *jobs.Job, b store.BackupJob, sharePath string) (string, error) {
	env, remote, err := s.rcloneRemote(ctx, b.Target, false)
	if err != nil {
		return "", err
	}
	verb := "copy" // never deletes on the destination
	if b.Mirror {
		verb = "sync"
	}
	local := strings.TrimSuffix(sharePath, "/") + "/"
	src, dst := local, remote
	if b.Direction == "pull" {
		src, dst = remote, local
	}
	args := []string{"--config", "", verb, src, dst, // --config "": in-memory config, only our env
		"--transfers", "2", "--checkers", "4", // gentle on an Atom and a home uplink
		"--stats", "10m", "--stats-log-level", "NOTICE", "--stats-one-line-date",
		"--exclude", ".recycle/**"}
	for _, e := range b.Excludes {
		args = append(args, "--exclude", e)
	}
	if b.Target.BandwidthKB > 0 {
		args = append(args, "--bwlimit", fmt.Sprintf("%dK", b.Target.BandwidthKB))
	}
	j.Logf("%s share %s %s %s (%s%s)", map[string]string{"push": "Archiving", "pull": "Restoring"}[b.Direction], b.Share,
		map[string]string{"push": "to", "pull": "from"}[b.Direction], describe(b.Target), b.Target.StorageClass,
		map[bool]string{true: ", encrypted", false: ""}[b.Target.EncryptPassword != ""])
	out, err := j.RunEnv(ctx, env, "rclone", args...)
	if err != nil {
		if msg := string(out) + err.Error(); b.Direction == "pull" && (strings.Contains(msg, "restore first") || strings.Contains(msg, "InvalidObjectState")) {
			return "", errors.New("the archived files aren't retrieved yet: use “Request retrieval” and wait (up to 48 h) before restoring")
		}
		return "", err
	}
	if b.Direction == "pull" {
		// Restored files: give them to the shares' group, like other copies.
		j.Run(ctx, "chgrp", "-R", "nasusers", sharePath)
		j.Run(ctx, "chmod", "-R", "g+rwX", sharePath)
	}
	return rcloneSummary(string(out)), nil
}

var rcloneStatRe = regexp.MustCompile(`([0-9.]+ ?[KMGTP]?i?B) / ([0-9.]+ ?[KMGTP]?i?B), `)

// rcloneSummary keeps the last statistics line ("12.3 MiB / 12.3 MiB, 100%, ...").
func rcloneSummary(out string) string {
	m := rcloneStatRe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		return "nothing new to copy"
	}
	return m[len(m)-1][2] + " transferred"
}

// Thaw asks the provider to make archived files readable again (S3
// RestoreObject), for `days` days. Bulk = the cheapest and slowest tier.
func (s *Service) Thaw(id int, days int) (jobs.Snapshot, error) {
	st := s.Store.Read()
	b := st.FindBackup(id)
	if b == nil || b.Target.Kind != "s3" {
		return jobs.Snapshot{}, errors.New("not a cloud archive job")
	}
	t := b.Target
	return s.Jobs.Start("Request retrieval: "+b.Name, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		env, remote, err := s.rcloneRemote(ctx, t, true) // the objects themselves, below the encryption
		if err != nil {
			return err
		}
		_, err = j.RunEnv(ctx, env, "rclone", "--config", "", "backend", "restore", remote, "-o", "priority=Bulk", "-o", fmt.Sprintf("lifetime=%d", days))
		if err == nil && s.Notify != nil {
			s.Notify("archive", "Retrieval requested for "+b.Name,
				"The provider is preparing the archived files (usually 12-48 h). Then run a restore job (direction: into the share).")
		}
		return err
	})
}
