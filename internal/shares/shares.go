// Package shares manages file-sharing users and SMB shares.
//
// The flow for every change is the same:
//
//	validate -> update state.json -> regenerate smb.conf -> testparm -> reload Samba
//
// state.json is the source of truth; smb.conf is derived from it and fully
// rewritten each time, so it can never drift from what the UI shows.
package shares

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"readynas/internal/run"
	"readynas/internal/store"
)

// Group owns all shared files; every NAS user is a member.
const Group = "nasusers"

type Service struct {
	Store     *store.Store
	Run       run.Func
	RunInput  run.InputFunc
	SmbConf   string // /etc/samba/smb.conf
	RsyncConf string // /etc/rsyncd.conf
	AvahiDir  string // /etc/avahi/services
	// Mounted returns volume name -> mountpoint for mounted volumes.
	Mounted func(ctx context.Context) (map[string]string, error)
	// Chown and GroupID touch the real system; tests replace them.
	Chown   func(path string, uid, gid int) error
	GroupID func(name string) (int, error)
	UserID  func(name string) (int, error)
	// QuotaMounts lists the mountpoints of volumes with quotas enabled.
	QuotaMounts func(ctx context.Context) ([]string, error)

	mu sync.Mutex // one change at a time: state + config files must agree
}

func NewService(st *store.Store, mounted func(context.Context) (map[string]string, error)) *Service {
	return &Service{
		Store: st, Run: run.Exec, RunInput: run.ExecInput,
		SmbConf: "/etc/samba/smb.conf", RsyncConf: "/etc/rsyncd.conf", AvahiDir: "/etc/avahi/services",
		Mounted: mounted, Chown: os.Chown, GroupID: lookupGID, UserID: lookupUID,
		QuotaMounts: func(context.Context) ([]string, error) { return nil, nil },
	}
}

func lookupUID(name string) (int, error) {
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

func lookupGID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(g.Gid)
}

// ------------------------------------------------------------------ users

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// Names that would be confusing or dangerous as NAS users.
var reservedUsers = []string{"admin", "root", "nobody", "guest", "daemon", "bin", "sys"}

const minPasswordLength = 8

type UserInfo struct {
	Name    string   `json:"name"`
	Shares  []string `json:"shares"` // shares this user can access
	QuotaMB int      `json:"quotaMB"`
}

func (s *Service) Users() []UserInfo {
	st := s.Store.Read()
	out := []UserInfo{}
	for _, u := range st.Users {
		if u.Admin {
			continue
		}
		info := UserInfo{Name: u.Name, Shares: []string{}, QuotaMB: u.QuotaMB}
		for _, sh := range st.Shares {
			if sh.Access[u.Name] != "" {
				info.Shares = append(info.Shares, sh.Name)
			}
		}
		out = append(out, info)
	}
	return out
}

func (s *Service) CreateUser(ctx context.Context, name, password string) error {
	if !userRe.MatchString(name) {
		return errors.New("user name must be 1-32 characters: lowercase letters, digits, - or _")
	}
	if slices.Contains(reservedUsers, name) {
		return fmt.Errorf("%q is reserved", name)
	}
	if len(password) < minPasswordLength {
		return errors.New("password must be at least 8 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.Store.Read()
	if st.FindUser(name) != nil {
		return fmt.Errorf("user %s already exists", name)
	}
	// The Linux account database is shared with the OS: never take over
	// an existing system account (like "nas", the VM's login user).
	if _, err := user.Lookup(name); err == nil {
		return fmt.Errorf("%q is already used by the system", name)
	}

	if _, err := s.Run(ctx, "groupadd", "--force", Group); err != nil {
		return err
	}
	// A Linux account is required for Samba to own files as this user. No
	// home and no shell: it can't log in, only use shares.
	if _, err := s.Run(ctx, "useradd", "--no-create-home", "--no-user-group", "--gid", Group,
		"--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "NAS user", name); err != nil {
		return err
	}
	if err := s.setSambaPassword(ctx, name, password); err != nil {
		s.Run(ctx, "userdel", name) // roll back: no half-created users
		return err
	}
	if err := s.Store.Update(func(st *store.State) error {
		st.Users = append(st.Users, store.User{Name: name})
		return nil
	}); err != nil {
		return err
	}
	if h := s.Store.Read().Settings.HomeShares; h.Enabled {
		if err := s.ensureHome(h.Path, name); err != nil {
			return fmt.Errorf("user created, but not their home folder: %w", err)
		}
	}
	return nil
}

// ------------------------------------------------------------ home shares

// SetHomeShares switches private home folders on or off. Turning them off
// only hides them: the folders and their files stay.
func (s *Service) SetHomeShares(ctx context.Context, enabled bool, volume string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := store.HomeShares{Enabled: enabled}
	if enabled {
		mounted, err := s.Mounted(ctx)
		if err != nil {
			return err
		}
		mnt, ok := mounted[volume]
		if !ok {
			return fmt.Errorf("volume %q doesn't exist or isn't mounted", volume)
		}
		h.Volume, h.Path = volume, filepath.Join(mnt, "homes")
		if err := os.MkdirAll(h.Path, 0o755); err != nil {
			return err
		}
		for _, u := range s.Users() {
			if err := s.ensureHome(h.Path, u.Name); err != nil {
				return err
			}
		}
	} else {
		h = s.Store.Read().Settings.HomeShares // keep the location for next time
		h.Enabled = false
	}
	if err := s.Store.Update(func(st *store.State) error { st.Settings.HomeShares = h; return nil }); err != nil {
		return err
	}
	return s.apply(ctx)
}

// ensureHome creates a user's private folder: only they can enter it.
func (s *Service) ensureHome(root, name string) error {
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	uid, err := s.UserID(name)
	if err != nil {
		return err
	}
	gid, err := s.GroupID(Group)
	if err != nil {
		return err
	}
	if err := s.Chown(dir, uid, gid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// ------------------------------------------------------------ quotas

// SetQuota sets how much space a user may use on each volume with quotas
// (0 = unlimited).
func (s *Service) SetQuota(ctx context.Context, name string, mb int) error {
	if mb < 0 || mb > 100_000_000 {
		return errors.New("invalid quota")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Store.Read(); !isShareUser(&st, name) {
		return fmt.Errorf("user %s not found", name)
	}
	if err := s.Store.Update(func(st *store.State) error {
		st.FindUser(name).QuotaMB = mb
		return nil
	}); err != nil {
		return err
	}
	return s.applyQuota(ctx, name, mb)
}

// ApplyQuotas sets every user's limit again (after quotas are enabled on
// a volume).
func (s *Service) ApplyQuotas(ctx context.Context) error {
	var errs []error
	for _, u := range s.Users() {
		errs = append(errs, s.applyQuota(ctx, u.Name, u.QuotaMB))
	}
	return errors.Join(errs...)
}

func (s *Service) applyQuota(ctx context.Context, name string, mb int) error {
	mounts, err := s.QuotaMounts(ctx)
	if err != nil {
		return err
	}
	for _, m := range mounts {
		// setquota user block-soft block-hard inode-soft inode-hard: in KiB,
		// hard limit only (a soft limit would allow a grace period above it).
		if _, err := s.Run(ctx, "setquota", "-u", name, "0", strconv.Itoa(mb*1024), "0", "0", m); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SetPassword(ctx context.Context, name, password string) error {
	if len(password) < minPasswordLength {
		return errors.New("password must be at least 8 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Store.Read(); !isShareUser(&st, name) {
		return fmt.Errorf("user %s not found", name)
	}
	return s.setSambaPassword(ctx, name, password)
}

// Samba keeps its own password database (SMB can't use Linux passwords).
// smbpasswd -s reads the new password twice from stdin.
func (s *Service) setSambaPassword(ctx context.Context, name, password string) error {
	_, err := s.RunInput(ctx, password+"\n"+password+"\n", "smbpasswd", "-a", "-s", name)
	return err
}

func (s *Service) DeleteUser(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Store.Read(); !isShareUser(&st, name) {
		return fmt.Errorf("user %s not found", name)
	}
	s.Run(ctx, "smbpasswd", "-x", name) // errors ignored: clean up as much as possible
	s.Run(ctx, "userdel", name)
	err := s.Store.Update(func(st *store.State) error {
		st.Users = slices.DeleteFunc(st.Users, func(u store.User) bool { return u.Name == name })
		for i := range st.Shares {
			delete(st.Shares[i].Access, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.apply(ctx)
}

// ----------------------------------------------------------------- shares

var shareRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,31}$`)

var reservedShares = []string{"global", "homes", "printers", "print$", "ipc$"}

// ShareRequest is what the UI sends. Name and Volume are only used on create.
type ShareRequest struct {
	Name         string            `json:"name"`
	Volume       string            `json:"volume"`
	Description  string            `json:"description"`
	Access       map[string]string `json:"access"`
	GuestRead    bool              `json:"guestRead"`
	RecycleBin   bool              `json:"recycleBin"`
	TimeMachine  bool              `json:"timeMachine"`
	RsyncRead    bool              `json:"rsyncRead"`
	AllowedHosts []string          `json:"allowedHosts"`
	Hidden       bool              `json:"hidden"`
	SMBDisabled  bool              `json:"smbDisabled"`
}

type ShareInfo struct {
	store.Share
	Available bool `json:"available"` // its volume is mounted
}

func (s *Service) Shares(ctx context.Context) ([]ShareInfo, error) {
	mounted, err := s.Mounted(ctx)
	if err != nil {
		return nil, err
	}
	out := []ShareInfo{}
	for _, sh := range s.Store.Read().Shares {
		_, ok := mounted[sh.Volume]
		if sh.Access == nil {
			sh.Access = map[string]string{}
		}
		out = append(out, ShareInfo{Share: sh, Available: ok})
	}
	return out, nil
}

// OnVolume lists the shares stored on a volume (to block deleting it).
func (s *Service) OnVolume(volume string) []string {
	var names []string
	st := s.Store.Read()
	if h := st.Settings.HomeShares; h.Enabled && h.Volume == volume {
		names = append(names, "home folders")
	}
	for _, sh := range st.Shares {
		if sh.Volume == volume {
			names = append(names, sh.Name)
		}
	}
	return names
}

func isShareUser(st *store.State, name string) bool {
	u := st.FindUser(name)
	return u != nil && !u.Admin
}

// validateSettings checks the fields that can change after creation.
func validateSettings(req ShareRequest, st store.State) error {
	if len(req.Description) > 100 || strings.ContainsAny(req.Description, "\n\r") {
		return errors.New("description must be a single line of at most 100 characters")
	}
	if req.TimeMachine && req.SMBDisabled {
		return errors.New("Time Machine works over SMB: keep SMB on for this share")
	}
	if len(req.AllowedHosts) > 10 {
		return errors.New("at most 10 allowed computers or networks")
	}
	for _, h := range req.AllowedHosts {
		if net.ParseIP(h) == nil {
			if _, _, err := net.ParseCIDR(h); err != nil {
				return fmt.Errorf("%q is not an IP address (192.168.1.20) or a network (192.168.1.0/24)", h)
			}
		}
	}
	for name, access := range req.Access {
		if access != "ro" && access != "rw" {
			return fmt.Errorf("access for %s must be ro or rw", name)
		}
		if !isShareUser(&st, name) {
			return fmt.Errorf("unknown user %s", name)
		}
	}
	return nil
}

func (s *Service) CreateShare(ctx context.Context, req ShareRequest) error {
	if !shareRe.MatchString(req.Name) {
		return errors.New("share name must be 1-32 characters: letters, digits, '.', '-' or '_'")
	}
	if slices.Contains(reservedShares, strings.ToLower(req.Name)) {
		return fmt.Errorf("%q is reserved by Samba", req.Name)
	}
	mounted, err := s.Mounted(ctx)
	if err != nil {
		return err
	}
	mnt, ok := mounted[req.Volume]
	if !ok {
		return fmt.Errorf("volume %q doesn't exist or isn't mounted", req.Volume)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Store.Read()
	if st.FindShare(req.Name) != nil {
		return fmt.Errorf("a share named %s already exists", req.Name)
	}
	if err := validateSettings(req, st); err != nil {
		return err
	}

	// Reuses the folder if it exists (e.g. re-creating a deleted share).
	path := filepath.Join(mnt, req.Name)
	if err := s.prepareFolder(path); err != nil {
		return err
	}
	err = s.Store.Update(func(st *store.State) error {
		st.Shares = append(st.Shares, store.Share{
			Name: req.Name, Volume: req.Volume, Path: path, Description: req.Description,
			Access: req.Access, GuestRead: req.GuestRead, RecycleBin: req.RecycleBin, TimeMachine: req.TimeMachine,
			RsyncRead: req.RsyncRead, AllowedHosts: req.AllowedHosts, Hidden: req.Hidden, SMBDisabled: req.SMBDisabled,
		})
		return nil
	})
	if err != nil {
		return err
	}
	return s.apply(ctx)
}

// prepareFolder creates the share folder: owned by root, group nasusers,
// mode 2775. The "2" (setgid) makes new files inherit the group.
func (s *Service) prepareFolder(path string) error {
	if err := os.MkdirAll(path, 0o775); err != nil {
		return err
	}
	gid, err := s.GroupID(Group)
	if err != nil {
		return fmt.Errorf("group %s missing: create a user first (%w)", Group, err)
	}
	if err := s.Chown(path, 0, gid); err != nil {
		return err
	}
	return os.Chmod(path, os.ModeSetgid|0o775)
}

func (s *Service) UpdateShare(ctx context.Context, name string, req ShareRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Store.Read()
	if st.FindShare(name) == nil {
		return fmt.Errorf("share %s not found", name)
	}
	if err := validateSettings(req, st); err != nil {
		return err
	}
	err := s.Store.Update(func(st *store.State) error {
		sh := st.FindShare(name)
		sh.Description, sh.Access, sh.GuestRead = req.Description, req.Access, req.GuestRead
		sh.RecycleBin, sh.TimeMachine = req.RecycleBin, req.TimeMachine
		sh.RsyncRead, sh.AllowedHosts, sh.Hidden, sh.SMBDisabled = req.RsyncRead, req.AllowedHosts, req.Hidden, req.SMBDisabled
		return nil
	})
	if err != nil {
		return err
	}
	return s.apply(ctx)
}

// DeleteShare stops sharing the folder. The files stay on the volume: deleting
// data should never be a side effect.
func (s *Service) DeleteShare(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.Store.Read(); st.FindShare(name) == nil {
		return fmt.Errorf("share %s not found", name)
	}
	err := s.Store.Update(func(st *store.State) error {
		st.Shares = slices.DeleteFunc(st.Shares, func(sh store.Share) bool { return strings.EqualFold(sh.Name, name) })
		return nil
	})
	if err != nil {
		return err
	}
	return s.apply(ctx)
}

// ---------------------------------------------------------- applying config

// Apply regenerates the config files from the current state. Called at
// startup so smb.conf always matches state.json.
func (s *Service) Apply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.apply(ctx)
}

func (s *Service) apply(ctx context.Context) error {
	st := s.Store.Read()

	// Keep Debian's original once, for reference.
	orig := s.SmbConf + ".orig"
	if _, err := os.Stat(orig); os.IsNotExist(err) {
		if data, err := os.ReadFile(s.SmbConf); err == nil {
			os.WriteFile(orig, data, 0o644)
		}
	}

	// Write next to the real file, validate it with testparm, then swap it
	// in atomically. A broken config never reaches Samba.
	tmp, err := os.CreateTemp(filepath.Dir(s.SmbConf), ".smb.conf-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(renderSmbConf(st)); err != nil {
		tmp.Close()
		return err
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	if _, err := s.Run(ctx, "testparm", "--suppress-prompt", tmp.Name()); err != nil {
		return fmt.Errorf("generated smb.conf is invalid: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.SmbConf); err != nil {
		return err
	}
	// SMB itself, then the two discovery services, each with its own switch.
	// Discovery announces the NAS; without SMB there is nothing to announce.
	set := st.Settings
	smbOn := !set.SMBDisabled
	if err := s.service(ctx, smbOn, "smbd", "nmbd"); err != nil {
		return err
	}
	if smbOn {
		// Running smbd processes re-read the config; open connections pick
		// up changes when they reconnect.
		if _, err := s.Run(ctx, "systemctl", "reload-or-restart", "smbd"); err != nil {
			return err
		}
	}
	if err := s.service(ctx, smbOn && !set.WSDDisabled, "wsdd2"); err != nil {
		return err
	}
	// Bonjour: avahi-daemon keeps running (it also answers for nas.local);
	// only our announcement file comes and goes. Avahi reloads by itself.
	avahiFile := filepath.Join(s.AvahiDir, "nasd-smb.service")
	if smbOn && !set.BonjourDisabled {
		if err := os.MkdirAll(s.AvahiDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(avahiFile, []byte(renderAvahi(st)), 0o644); err != nil {
			return err
		}
	} else {
		os.Remove(avahiFile)
	}

	// rsync daemon: reads its config at each connection, no reload needed.
	if err := os.WriteFile(s.RsyncConf, []byte(renderRsyncd(st)), 0o644); err != nil {
		return err
	}
	return s.service(ctx, set.RsyncEnabled, "rsync")
}

// service enables and starts systemd units, or stops and disables them.
func (s *Service) service(ctx context.Context, on bool, units ...string) error {
	action := "disable"
	if on {
		action = "enable"
	}
	_, err := s.Run(ctx, "systemctl", append([]string{action, "--now"}, units...)...)
	return err
}

// SetSettings switches protocols and discovery services on or off.
func (s *Service) SetSettings(ctx context.Context, settings store.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only the protocol switches: other settings (home shares, USB copy)
	// have their own endpoints and must not be overwritten by a stale copy.
	if err := s.Store.Update(func(st *store.State) error {
		st.Settings.SMBDisabled, st.Settings.RsyncEnabled = settings.SMBDisabled, settings.RsyncEnabled
		st.Settings.WSDDisabled, st.Settings.BonjourDisabled = settings.WSDDisabled, settings.BonjourDisabled
		return nil
	}); err != nil {
		return err
	}
	return s.apply(ctx)
}

// ------------------------------------------------------------- services

type ServiceStatus struct {
	Unit   string `json:"unit"`
	Label  string `json:"label"`
	Active bool   `json:"active"`
}

func (s *Service) Status(ctx context.Context) []ServiceStatus {
	units := []ServiceStatus{
		{Unit: "smbd", Label: "File sharing (SMB)"},
		{Unit: "wsdd2", Label: "Windows discovery"},
		{Unit: "avahi-daemon", Label: "macOS discovery (Bonjour)"},
		{Unit: "rsync", Label: "Backup server (rsync)"},
	}
	for i := range units {
		out, _ := s.Run(ctx, "systemctl", "is-active", units[i].Unit)
		units[i].Active = strings.TrimSpace(string(out)) == "active"
	}
	return units
}
