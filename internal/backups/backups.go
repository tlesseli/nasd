// Package backups copies shares to and from other places with rsync:
// a USB disk, another computer over SSH, or an rsync server (like an old
// ReadyNAS).
//
// RAID protects against a dead disk. Backups protect against everything
// else: a deleted folder, ransomware, a fire, or the NAS itself dying. The
// classic rule is 3-2-1: 3 copies, on 2 kinds of media, 1 of them elsewhere.
//
// rsync only transfers what changed, so after the first run a backup of
// unchanged data takes seconds even for terabytes.
package backups

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/run"
	"readynas/internal/store"
)

type Service struct {
	Store     *store.Store
	Jobs      *jobs.Manager
	Run       run.Func // for quick commands outside jobs (tests, key generation)
	DataDir   string   // /var/lib/nasd: SSH key, known_hosts, temporary password files
	MountBase string   // /run/nasd/usb: where USB disks are mounted during a job
	Now       func() time.Time
	// Notify raises an alert (nil in tests).
	Notify func(kind, subject, body string)

	mu        sync.Mutex
	lastFired map[int]time.Time // scheduler: job id -> minute it was last triggered
	pending   map[int]time.Time // scheduler: due jobs waiting for the NAS to be free
}

func NewService(st *store.Store, jm *jobs.Manager, dataDir string) *Service {
	return &Service{
		Store: st, Jobs: jm, Run: run.Exec, DataDir: dataDir, MountBase: "/run/nasd/usb", Now: time.Now,
		lastFired: map[int]time.Time{}, pending: map[int]time.Time{},
	}
}

// ------------------------------------------------------------ validation

var (
	hostRe   = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,252})$`)
	userRe   = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,31}$`)
	moduleRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	uuidRe   = regexp.MustCompile(`^[A-Za-z0-9-]{4,64}$`)
)

// Validate normalizes a job (default ports and paths) and checks it.
func Validate(j *store.BackupJob, st *store.State) error {
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" || len(j.Name) > 40 || strings.ContainsAny(j.Name, "\n\r") {
		return errors.New("name must be 1-40 characters")
	}
	if j.Direction != "push" && j.Direction != "pull" {
		return errors.New("direction must be push or pull")
	}
	if st.FindShare(j.Share) == nil {
		return fmt.Errorf("share %q not found", j.Share)
	}
	if err := ValidateTarget(&j.Target, j.Share); err != nil {
		return err
	}

	var excludes []string
	for _, e := range j.Excludes {
		if e = strings.TrimSpace(e); e != "" {
			excludes = append(excludes, e)
		}
	}
	if len(excludes) > 50 {
		return errors.New("at most 50 exclusions")
	}
	j.Excludes = excludes

	s := j.Schedule
	switch {
	case !slices.Contains([]string{"", "hourly", "daily", "weekly"}, s.Kind):
		return errors.New("unknown schedule")
	case s.Minute < 0 || s.Minute > 59 || s.Hour < 0 || s.Hour > 23 || s.Weekday < 0 || s.Weekday > 6:
		return errors.New("invalid schedule time")
	}
	return nil
}

// ValidateTarget normalizes and checks where a backup goes. share is used
// for the default folder on USB disks.
func ValidateTarget(t *store.BackupTarget, share string) error {
	t.Path = strings.TrimSpace(t.Path)
	if strings.ContainsAny(t.Path, "\n\r") || strings.Contains("/"+t.Path+"/", "/../") {
		return errors.New("the path can't contain '..' or line breaks")
	}
	switch t.Kind {
	case "usb":
		if !uuidRe.MatchString(t.USBUUID) {
			return errors.New("choose a USB disk")
		}
		t.Path = strings.Trim(t.Path, "/")
		if t.Path == "" {
			t.Path = "nas-backup/" + share
		}
		t.Host, t.User, t.Password, t.Module, t.Port = "", "", "", "", 0
	case "ssh":
		if err := checkHost(t.Host); err != nil {
			return err
		}
		if !userRe.MatchString(t.User) {
			return errors.New("enter the user name on the other computer")
		}
		if !strings.HasPrefix(t.Path, "/") {
			return errors.New("the folder on the other computer must be an absolute path, like /backups/nas")
		}
		if t.Port == 0 {
			t.Port = 22
		}
		t.USBUUID, t.Password, t.Module = "", "", ""
	case "rsync":
		if err := checkHost(t.Host); err != nil {
			return err
		}
		if !moduleRe.MatchString(t.Module) {
			return errors.New("enter the rsync module (on a ReadyNAS: the share name)")
		}
		if t.User != "" && !userRe.MatchString(t.User) {
			return errors.New("invalid user name")
		}
		t.Path = strings.Trim(t.Path, "/")
		if t.Port == 0 {
			t.Port = 873
		}
		t.USBUUID = ""
	default:
		return errors.New("choose where to back up: usb, ssh or rsync")
	}
	if t.Port < 0 || t.Port > 65535 {
		return errors.New("invalid port")
	}
	return nil
}

func checkHost(h string) error {
	if net.ParseIP(h) == nil && !hostRe.MatchString(h) {
		return errors.New("enter the other computer's IP address or host name")
	}
	return nil
}

// ------------------------------------------------------------ CRUD

func (s *Service) List() []store.BackupJob {
	st := s.Store.Read()
	out := st.Backups
	for i := range out {
		out[i].Target.Password = "" // never sent back to the browser
	}
	if out == nil {
		out = []store.BackupJob{}
	}
	return out
}

// Save creates (ID 0) or updates a job. An empty password on update keeps
// the stored one, since the UI never receives it.
func (s *Service) Save(j store.BackupJob) (int, error) {
	var id int
	err := s.Store.Update(func(st *store.State) error {
		if err := Validate(&j, st); err != nil {
			return err
		}
		if j.ID == 0 {
			st.NextBackupID++
			j.ID = st.NextBackupID
			j.LastRun = store.BackupRun{}
			st.Backups = append(st.Backups, j)
		} else {
			old := st.FindBackup(j.ID)
			if old == nil {
				return fmt.Errorf("backup job %d not found", j.ID)
			}
			if j.Target.Kind == "rsync" && j.Target.Password == "" && old.Target.Kind == "rsync" {
				j.Target.Password = old.Target.Password
			}
			j.LastRun = old.LastRun
			*old = j
		}
		id = j.ID
		return nil
	})
	return id, err
}

func (s *Service) Delete(id int) error {
	return s.Store.Update(func(st *store.State) error {
		if st.FindBackup(id) == nil {
			return fmt.Errorf("backup job %d not found", id)
		}
		st.Backups = slices.DeleteFunc(st.Backups, func(b store.BackupJob) bool { return b.ID == id })
		return nil
	})
}

// SharesUsedBy lists backup jobs using a share (to block deleting it).
func SharesUsedBy(st *store.State, share string) []string {
	var names []string
	for _, b := range st.Backups {
		if strings.EqualFold(b.Share, share) {
			names = append(names, b.Name)
		}
	}
	return names
}

// ------------------------------------------------------------ helpers

// mountDir is where a USB filesystem is mounted while a job runs.
func (s *Service) mountDir(uuid string) string {
	return filepath.Join(s.MountBase, uuid)
}

func (s *Service) keyPath() string        { return filepath.Join(s.DataDir, "backup_ed25519") }
func (s *Service) knownHostsPath() string { return filepath.Join(s.DataDir, "known_hosts") }

// PublicKey returns the NAS's SSH public key, creating the key pair on first
// use. Add it to ~/.ssh/authorized_keys on the computer receiving backups.
func (s *Service) PublicKey(ctx context.Context) (string, error) {
	if _, err := os.Stat(s.keyPath()); os.IsNotExist(err) {
		host, _ := os.Hostname()
		if _, err := s.Run(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "nasd@"+host, "-f", s.keyPath()); err != nil {
			return "", err
		}
	}
	b, err := os.ReadFile(s.keyPath() + ".pub")
	return strings.TrimSpace(string(b)), err
}
