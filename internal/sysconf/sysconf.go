// Package sysconf changes system settings: hostname, time, restart and
// shutdown, OS updates. Network settings live in network.go.
package sysconf

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
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
	Store      *store.Store
	Jobs       *jobs.Manager
	Run        run.Func
	NetworkDir string // /etc/systemd/network
	Hosts      string // /etc/hosts
	Timesyncd  string // /etc/systemd/timesyncd.conf.d/nasd.conf
	Sys        string // /sys
	Now        func() time.Time
	Notify     func(kind, subject, body string)
	// RestartSelf restarts nasd (to pick up a new time zone or hostname).
	RestartSelf func()
	// ConfirmWindow is how long a network change waits for confirmation.
	ConfirmWindow time.Duration

	mu       sync.Mutex
	rollback *time.Timer
	zones    []string
}

func NewService(st *store.Store, j *jobs.Manager) *Service {
	s := &Service{
		Store: st, Jobs: j, Run: run.Exec, NetworkDir: "/etc/systemd/network", Hosts: "/etc/hosts",
		Timesyncd: "/etc/systemd/timesyncd.conf.d/nasd.conf", Sys: "/sys", Now: time.Now,
		Notify: func(string, string, string) {}, ConfirmWindow: 90 * time.Second,
	}
	s.RestartSelf = func() {
		go func() {
			time.Sleep(time.Second) // let the HTTP answer go out
			s.Run(context.Background(), "systemctl", "restart", "nasd")
		}()
	}
	return s
}

// ------------------------------------------------------------ hostname

// 15 characters max: the old NetBIOS name (what Windows shows) can't be
// longer, and Samba derives it from the hostname.
var hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,13}[a-z0-9])?$`)

func (s *Service) SetHostname(ctx context.Context, name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if !hostnameRe.MatchString(name) {
		return errors.New("1-15 characters: letters, digits and '-' (not at the start or end)")
	}
	if _, err := s.Run(ctx, "hostnamectl", "set-hostname", name); err != nil {
		return err
	}
	// Debian maps the hostname to 127.0.1.1 in /etc/hosts; keep it in sync,
	// or tools like sudo complain they can't resolve the machine's name.
	data, _ := os.ReadFile(s.Hosts)
	var lines []string
	found := false
	for _, l := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if strings.HasPrefix(l, "127.0.1.1") {
			l, found = "127.0.1.1\t"+name, true
		}
		lines = append(lines, l)
	}
	if !found {
		lines = append(lines, "127.0.1.1\t"+name)
	}
	if err := os.WriteFile(s.Hosts, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	// cloud-init (dev VM only) would put the old name back at boot.
	if _, err := os.Stat("/etc/cloud/cloud.cfg.d"); err == nil {
		os.WriteFile("/etc/cloud/cloud.cfg.d/99-nasd-hostname.cfg", []byte("preserve_hostname: true\n"), 0o644)
	}
	// Bonjour and Samba announce the name: restart them.
	s.Run(ctx, "systemctl", "try-restart", "avahi-daemon", "smbd", "nmbd", "wsdd2")
	s.RestartSelf() // new HTTPS certificate for the new name
	return nil
}

// ------------------------------------------------------------ time

type TimeInfo struct {
	Now        time.Time `json:"now"`
	Timezone   string    `json:"timezone"`
	NTP        bool      `json:"ntp"`
	Synced     bool      `json:"synced"`
	NTPServers []string  `json:"ntpServers"` // empty = Debian's default pool
}

func (s *Service) Time(ctx context.Context) (TimeInfo, error) {
	out, err := s.Run(ctx, "timedatectl", "show")
	if err != nil {
		return TimeInfo{}, err
	}
	kv := map[string]string{}
	for _, l := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			kv[k] = v
		}
	}
	info := TimeInfo{Now: s.Now(), Timezone: kv["Timezone"], NTP: kv["NTP"] == "yes", Synced: kv["NTPSynchronized"] == "yes", NTPServers: []string{}}
	if data, err := os.ReadFile(s.Timesyncd); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if v, ok := strings.CutPrefix(l, "NTP="); ok {
				info.NTPServers = strings.Fields(v)
			}
		}
	}
	return info, nil
}

func (s *Service) Timezones(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.zones == nil {
		out, err := s.Run(ctx, "timedatectl", "list-timezones")
		if err != nil {
			return nil, err
		}
		s.zones = strings.Fields(string(out))
	}
	return s.zones, nil
}

type TimeRequest struct {
	Timezone   string   `json:"timezone"`
	NTP        bool     `json:"ntp"`
	NTPServers []string `json:"ntpServers"`
	Manual     string   `json:"manual"` // "2006-01-02 15:04:05", only without NTP
}

var ntpServerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,252}$`)

func (s *Service) SetTime(ctx context.Context, req TimeRequest) error {
	zones, err := s.Timezones(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(zones, req.Timezone) {
		return fmt.Errorf("unknown time zone %q", req.Timezone)
	}
	var servers []string
	for _, srv := range req.NTPServers {
		if srv = strings.TrimSpace(srv); srv == "" {
			continue
		}
		if !ntpServerRe.MatchString(srv) {
			return fmt.Errorf("%q is not a valid server name", srv)
		}
		servers = append(servers, srv)
	}
	if !req.NTP && req.Manual != "" {
		if _, err := time.Parse("2006-01-02 15:04:05", req.Manual); err != nil {
			return errors.New("date must look like 2026-11-08 20:15:00")
		}
	}

	before, _ := s.Time(ctx)
	if _, err := s.Run(ctx, "timedatectl", "set-timezone", req.Timezone); err != nil {
		return err
	}
	if len(servers) > 0 {
		os.MkdirAll(dirOf(s.Timesyncd), 0o755)
		content := "# Generated by nasd\n[Time]\nNTP=" + strings.Join(servers, " ") + "\n"
		if err := os.WriteFile(s.Timesyncd, []byte(content), 0o644); err != nil {
			return err
		}
	} else {
		os.Remove(s.Timesyncd)
	}
	if _, err := s.Run(ctx, "timedatectl", "set-ntp", fmt.Sprint(req.NTP)); err != nil {
		return err
	}
	if req.NTP {
		s.Run(ctx, "systemctl", "restart", "systemd-timesyncd")
	} else if req.Manual != "" {
		if _, err := s.Run(ctx, "timedatectl", "set-time", req.Manual); err != nil {
			return err
		}
	}
	// Go reads the time zone once at startup: schedules (backups, checks,
	// power) would keep using the old one until nasd restarts.
	if before.Timezone != req.Timezone {
		s.RestartSelf()
	}
	return nil
}

func dirOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "/"
	}
	return path[:i]
}

// ------------------------------------------------------------ power

// Power restarts or shuts down the NAS, refusing while a disk operation
// runs (unless forced).
func (s *Service) Power(ctx context.Context, action string, force bool) error {
	if action != "reboot" && action != "poweroff" {
		return errors.New("action must be reboot or poweroff")
	}
	if s.Jobs.Busy() && !force {
		return errors.New("a disk operation is running: wait for it, or force")
	}
	s.Notify("power_action", map[string]string{"reboot": "Restart requested", "poweroff": "Shutdown requested"}[action], "Requested from the web interface.")
	go func() {
		time.Sleep(2 * time.Second) // answer the browser and save the event first
		if _, err := s.Run(context.Background(), "systemctl", action); err != nil {
			log.Printf("%s failed: %v", action, err)
		}
	}()
	return nil
}

// ------------------------------------------------------------ updates

type Update struct {
	Package string `json:"package"`
	From    string `json:"from"`
	To      string `json:"to"`
}

type UpdatesInfo struct {
	Updates        []Update `json:"updates"`
	RebootRequired bool     `json:"rebootRequired"`
}

var aptLineRe = regexp.MustCompile(`^([^/\s]+)/\S+\s+(\S+)\s+\S+\s+\[upgradable from: ([^\]]+)\]`)

// Updates lists packages with a newer version (as of the last check).
func (s *Service) Updates(ctx context.Context) (UpdatesInfo, error) {
	out, err := s.Run(ctx, "apt", "list", "--upgradable")
	if err != nil {
		return UpdatesInfo{}, err
	}
	info := UpdatesInfo{Updates: []Update{}}
	for _, l := range strings.Split(string(out), "\n") {
		if m := aptLineRe.FindStringSubmatch(l); m != nil {
			info.Updates = append(info.Updates, Update{Package: m[1], To: m[2], From: m[3]})
		}
	}
	_, err = os.Stat("/run/reboot-required")
	info.RebootRequired = err == nil
	return info, nil
}

// CheckUpdates refreshes the package lists (a job: it downloads).
func (s *Service) CheckUpdates(ctx context.Context, r run.Func) error {
	_, err := r(ctx, "apt-get", "update", "-q")
	return err
}

// InstallUpdates upgrades installed packages. --force-confold keeps our
// generated config files (smb.conf...) when a package ships a new default.
func (s *Service) InstallUpdates(ctx context.Context, r run.Func) error {
	_, err := r(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "-q",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "upgrade")
	return err
}
