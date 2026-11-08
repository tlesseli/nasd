// Package store keeps nasd's own state (users, later shares and settings)
// in a single JSON file. A NAS has a handful of users and shares, so a JSON
// file is simpler than a database and you can read it with `cat`.
//
// Writes are atomic: we write a temp file, fsync it, then rename() it over
// the old one. rename() is atomic on Linux, so a power cut leaves either the
// old or the new file, never a half-written one.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

type User struct {
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
	Admin        bool   `json:"admin"`
	QuotaMB      int    `json:"quotaMB"` // max space per volume (ext4 with quotas); 0 = unlimited
}

// Share is a folder on a volume, exported over SMB.
type Share struct {
	Name        string `json:"name"`
	Volume      string `json:"volume"`
	Path        string `json:"path"`
	Description string `json:"description"`
	// Access maps a user name to "ro" (read only) or "rw" (read & write).
	Access      map[string]string `json:"access"`
	GuestRead   bool              `json:"guestRead"` // anyone on the LAN can read, no password
	RecycleBin  bool              `json:"recycleBin"`
	TimeMachine bool              `json:"timeMachine"`
	RsyncRead   bool              `json:"rsyncRead"` // readable by the rsync service (for backups by another machine)
	Hidden      bool              `json:"hidden"`    // works, but not listed when browsing the NAS
	// SMBDisabled: not offered over SMB (e.g. an rsync-only backup source).
	// "Disabled" so that shares saved before this option keep SMB.
	SMBDisabled bool `json:"smbDisabled"`
	// AllowedHosts restricts SMB and rsync access to these IPs / networks
	// (empty = the whole LAN).
	AllowedHosts []string `json:"allowedHosts"`
}

// Settings are global switches. Note the "Disabled" name: a missing field in
// an older state.json reads as false, so the safe default (SMB on) must be
// the zero value.
type Settings struct {
	SMBDisabled  bool `json:"smbDisabled"`
	RsyncEnabled bool `json:"rsyncEnabled"`
	// Discovery announces the NAS (not single shares) on the network.
	WSDDisabled     bool       `json:"wsdDisabled"`     // Windows (WS-Discovery, wsdd2)
	BonjourDisabled bool       `json:"bonjourDisabled"` // macOS (Bonjour / mDNS, Avahi)
	HomeShares      HomeShares `json:"homeShares"`
	USBCopy         USBCopy    `json:"usbCopy"`
}

// HomeShares gives every user a private share named after them.
type HomeShares struct {
	Enabled bool   `json:"enabled"`
	Volume  string `json:"volume"`
	Path    string `json:"path"` // /data/<volume>/homes
}

// USBCopy copies the content of USB disks into a share when plugged in.
type USBCopy struct {
	Enabled bool   `json:"enabled"`
	Share   string `json:"share"`
	Path    string `json:"path"`  // sub-folder in the share
	Owner   string `json:"owner"` // user owning the copied files ("" = shared)
}

// BackupJob copies a share to or from somewhere else with rsync.
type BackupJob struct {
	ID        int          `json:"id"`
	Name      string       `json:"name"`
	Direction string       `json:"direction"` // "push" (share -> target) or "pull" (target -> share)
	Share     string       `json:"share"`
	Target    BackupTarget `json:"target"`
	Mirror    bool         `json:"mirror"` // delete files on the destination that are gone from the source
	Excludes  []string     `json:"excludes"`
	Schedule  Schedule     `json:"schedule"`
	LastRun   BackupRun    `json:"lastRun"`
}

type BackupTarget struct {
	Kind     string `json:"kind"`               // "usb", "ssh" or "rsync"
	USBUUID  string `json:"usbUUID,omitempty"`  // usb: filesystem UUID
	Host     string `json:"host,omitempty"`     // ssh, rsync
	Port     int    `json:"port,omitempty"`     // ssh (22), rsync (873)
	User     string `json:"user,omitempty"`     // ssh; optional for rsync
	Password string `json:"password,omitempty"` // rsync only (ssh uses the NAS key)
	Module   string `json:"module,omitempty"`   // rsync
	Path     string `json:"path"`               // folder on the target
}

type Schedule struct {
	Kind    string `json:"kind"`    // "" (manual), "hourly", "daily", "weekly"
	Weekday int    `json:"weekday"` // 0 = Sunday (weekly)
	Hour    int    `json:"hour"`    // daily, weekly
	Minute  int    `json:"minute"`
}

// Due reports whether the schedule fires at minute t.
func (s Schedule) Due(t time.Time) bool {
	if t.Minute() != s.Minute {
		return false
	}
	switch s.Kind {
	case "hourly":
		return true
	case "daily":
		return t.Hour() == s.Hour
	case "weekly":
		return t.Hour() == s.Hour && int(t.Weekday()) == s.Weekday
	}
	return false
}

// AlertSettings: who gets emails and how they are sent.
type AlertSettings struct {
	Emails   []string `json:"emails"` // up to 3
	SMTP     SMTP     `json:"smtp"`
	Disabled []string `json:"disabled"` // optional event kinds switched off
	// Temperature (°C) above which a disk triggers an alert; 0 = default.
	DiskTempLimit int `json:"diskTempLimit"`
	// Volume usage (%) above which an alert is sent; 0 = default.
	VolumeFullPercent int `json:"volumeFullPercent"`
}

type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"` // "starttls", "tls" or "none"
	User     string `json:"user"`
	Password string `json:"password,omitempty"`
	From     string `json:"from"`
}

// Event is something worth telling the owner about.
type Event struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"` // "critical", "warning" or "info"
	Subject  string    `json:"subject"`
	Body     string    `json:"body"`
	Emailed  bool      `json:"emailed"`
	EmailErr string    `json:"emailError,omitempty"`
}

// Maintenance of one volume: a scheduled consistency check ("scrub").
type Maintenance struct {
	Scrub   Schedule `json:"scrub"`
	AutoFix bool     `json:"autoFix"` // repair parity mismatches found by a check
	// FsckAtBoot: an ext4 check was requested for the next boot; nasd
	// restores the normal settings once the NAS has restarted.
	FsckAtBoot bool `json:"fsckAtBoot"`
}

// PowerSettings: saving energy and surviving power cuts.
type PowerSettings struct {
	SpinDownMinutes int        `json:"spinDownMinutes"` // 0 = disks never sleep
	Timer           PowerTimer `json:"timer"`
	UPS             UPS        `json:"ups"`
	WakeOnLAN       bool       `json:"wakeOnLan"`
}

// PowerTimer switches the NAS off on a schedule and back on at a time
// (through the motherboard's real-time clock alarm).
type PowerTimer struct {
	Off      Schedule `json:"off"`      // daily or weekly; Kind "" = disabled
	OnHour   int      `json:"onHour"`   // power back on at the next OnHour:OnMinute
	OnMinute int      `json:"onMinute"` //
}

type UPS struct {
	Enabled bool   `json:"enabled"`
	Driver  string `json:"driver"` // usbhid-ups (most USB UPS), nutdrv_qx, dummy-ups (simulation)
}

// ButtonSettings: what the front Backup button does.
type ButtonSettings struct {
	Device string `json:"device"` // input device name that sends the press (learned)
	Code   int    `json:"code"`   // key code (learned)
	Jobs   []int  `json:"jobs"`   // backup jobs to run, in order
}

// FanSettings: who drives the fan.
type FanSettings struct {
	Mode       string `json:"mode"`       // "" = the motherboard (default), "curve" = nasd, "full" = always 100%
	PWM        string `json:"pwm"`        // controlled output, e.g. "hwmon2/pwm1"
	MinPercent int    `json:"minPercent"` // speed at or below LowTemp
	LowTemp    int    `json:"lowTemp"`    // °C (hottest disk)
	HighTemp   int    `json:"highTemp"`   // °C: 100% at or above
}

// NetworkSettings: Managed=false leaves the network to the OS (DHCP).
type NetworkSettings struct {
	Managed    bool     `json:"managed"`
	Interfaces []string `json:"interfaces"` // 1 interface, or 2+ when bonded
	Bond       string   `json:"bond"`       // "" (no bonding), active-backup, balance-alb, 802.3ad
	DHCP       bool     `json:"dhcp"`
	Address    string   `json:"address"` // static: 192.168.1.50/24
	Gateway    string   `json:"gateway"`
	DNS        []string `json:"dns"`
	MTU        int      `json:"mtu"` // 0 = default (1500); 9000 = jumbo frames
}

// NetworkChange is an applied but not yet confirmed network configuration.
// If nobody confirms it before the deadline, the previous one comes back.
type NetworkChange struct {
	Previous NetworkSettings `json:"previous"`
	Deadline time.Time       `json:"deadline"`
}

type BackupRun struct {
	Status  string    `json:"status"` // "" (never ran), "done", "failed"
	Started time.Time `json:"started,omitzero"`
	Ended   time.Time `json:"ended,omitzero"`
	Summary string    `json:"summary"`
}

// State is everything nasd persists. New fields get added here as the
// project grows (alert settings, ...).
//
// Users with Admin=true log into the web UI (only "admin" for now); the
// others are file-sharing accounts: they exist as Linux + Samba users and
// can't log into the UI.
type State struct {
	Users          []User                 `json:"users"`
	Shares         []Share                `json:"shares"`
	Settings       Settings               `json:"settings"`
	Backups        []BackupJob            `json:"backups"`
	NextBackupID   int                    `json:"nextBackupId"`
	Alerts         AlertSettings          `json:"alerts"`
	Events         []Event                `json:"events"`      // most recent last, capped
	Maintenance    map[string]Maintenance `json:"maintenance"` // by volume name
	Power          PowerSettings          `json:"power"`
	Network        NetworkSettings        `json:"network"`
	NetworkPending *NetworkChange         `json:"networkPending,omitempty"`
	Button         ButtonSettings         `json:"button"`
	Fan            FanSettings            `json:"fan"`
}

func (s *State) FindBackup(id int) *BackupJob {
	for i := range s.Backups {
		if s.Backups[i].ID == id {
			return &s.Backups[i]
		}
	}
	return nil
}

func (s *State) FindShare(name string) *Share {
	for i := range s.Shares {
		if strings.EqualFold(s.Shares[i].Name, name) { // SMB names are case-insensitive
			return &s.Shares[i]
		}
	}
	return nil
}

func (s *State) FindUser(name string) *User {
	for i := range s.Users {
		if s.Users[i].Name == name {
			return &s.Users[i]
		}
	}
	return nil
}

type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil // first start: empty state
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Read gives fn a copy of the state, so callers can't mutate it by accident.
func (s *Store) Read() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state)
}

// Update runs fn on a copy of the state and persists it. If fn returns an
// error (or saving fails), nothing changes: in memory or on disk.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) save(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())              // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil { // contains password hashes
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// clone makes a deep copy: slices and maps would otherwise be shared, and
// Update's "nothing changes on error" promise would silently break.
func clone(st State) State {
	out := st
	out.Users = append([]User(nil), st.Users...)
	out.Shares = make([]Share, len(st.Shares))
	for i, sh := range st.Shares {
		sh.Access = maps.Clone(sh.Access)
		sh.AllowedHosts = slices.Clone(sh.AllowedHosts)
		out.Shares[i] = sh
	}
	out.Alerts.Emails = slices.Clone(st.Alerts.Emails)
	out.Alerts.Disabled = slices.Clone(st.Alerts.Disabled)
	out.Events = slices.Clone(st.Events)
	out.Maintenance = maps.Clone(st.Maintenance)
	out.Button.Jobs = slices.Clone(st.Button.Jobs)
	out.Network.Interfaces = slices.Clone(st.Network.Interfaces)
	out.Network.DNS = slices.Clone(st.Network.DNS)
	if st.NetworkPending != nil {
		p := *st.NetworkPending
		p.Previous.Interfaces = slices.Clone(p.Previous.Interfaces)
		p.Previous.DNS = slices.Clone(p.Previous.DNS)
		out.NetworkPending = &p
	}
	out.Backups = make([]BackupJob, len(st.Backups))
	for i, b := range st.Backups {
		b.Excludes = slices.Clone(b.Excludes)
		out.Backups[i] = b
	}
	return out
}
