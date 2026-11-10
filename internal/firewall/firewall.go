// Package firewall keeps the NAS reachable only from the networks it serves.
//
// The NAS is meant to stay on a home or office network, behind a router.
// The firewall makes that true even when it isn't: a forwarded port, a
// router that lets IPv6 in, a guest network... Connections from allowed
// networks are accepted, whatever the port; everything else that tries to
// start a connection is dropped (and counted, and the first few logged).
//
// Lockout protection, because the NAS has no screen to fix things on:
//   - a change is refused if it would block the computer making it;
//   - a change applies at once but must be kept within ConfirmWindow,
//     otherwise the previous settings come back (like network changes);
//   - only kept settings are loaded at boot (nasd-firewall.service);
//   - with the "local" scope, the NAS's own networks are followed: if the
//     router hands out another address range, the rules follow within a
//     minute;
//   - holding the Reset button at the back switches the firewall off.
//
// The rules live in their own nftables table (inet nasd), so nothing else
// on the system is touched.
package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"readynas/internal/run"
	"readynas/internal/store"
)

const (
	table     = "nasd"
	logPrefix = "nasd-firewall: "
	maxAllow  = 32
)

// Private ranges: home and office networks, link-local, IPv6 unique local.
var privateNets = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
	"fc00::/7", "fe80::/10",
}

type Service struct {
	Store *store.Store
	Run   run.Func
	// RunEnv installs nftables when it is missing.
	RunEnv run.EnvFunc
	// RulesFile is loaded at boot by UnitFile (only kept settings go there).
	RulesFile string // /etc/nasd/firewall.nft
	UnitFile  string // /etc/systemd/system/nasd-firewall.service
	NftPath   string // /usr/sbin/nft
	// LocalNets returns the networks the NAS is on (its addresses' prefixes).
	LocalNets     func() []netip.Prefix
	Now           func() time.Time
	Notify        func(kind, subject, body string)
	ConfirmWindow time.Duration

	mu       sync.Mutex
	rollback *time.Timer
	applied  string // rules currently loaded ("" = none)
}

func NewService(st *store.Store) *Service {
	return &Service{
		Store: st, Run: run.Exec, RunEnv: run.ExecEnv,
		RulesFile: "/etc/nasd/firewall.nft", UnitFile: "/etc/systemd/system/nasd-firewall.service",
		NftPath: "/usr/sbin/nft", LocalNets: localNets, Now: time.Now,
		Notify: func(string, string, string) {}, ConfirmWindow: 2 * time.Minute,
	}
}

// ------------------------------------------------------------ rules

// Normalize checks and cleans settings: a valid scope, allowed entries as
// networks ("192.168.68.7" becomes "192.168.68.7/32"), no duplicates.
func Normalize(f *store.FirewallSettings) error {
	if f.Scope == "" {
		f.Scope = "private"
	}
	if f.Scope != "private" && f.Scope != "local" {
		return fmt.Errorf("unknown scope %q", f.Scope)
	}
	var out []string
	for _, a := range f.Allow {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		p, err := parseNet(a)
		if err != nil {
			return fmt.Errorf("%s: not an address or a network (like 192.168.68.0/22)", a)
		}
		if p.Bits() == 0 {
			return fmt.Errorf("%s is the whole internet: switch the firewall off instead", a)
		}
		if s := p.String(); !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	if len(out) > maxAllow {
		return fmt.Errorf("at most %d networks", maxAllow)
	}
	f.Allow = out
	return nil
}

func parseNet(s string) (netip.Prefix, error) {
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		a = a.Unmap().WithZone("")
		return netip.PrefixFrom(a, a.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return p, err
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()).Masked(), nil
}

// Allowed returns the networks a configuration lets in.
func Allowed(f store.FirewallSettings, local []netip.Prefix) []netip.Prefix {
	var nets []netip.Prefix
	if f.Scope == "local" {
		nets = append(nets, netip.MustParsePrefix("fe80::/10"))
		for _, p := range local {
			nets = append(nets, p.Masked())
		}
	} else {
		for _, s := range privateNets {
			nets = append(nets, netip.MustParsePrefix(s))
		}
	}
	for _, a := range f.Allow {
		if p, err := parseNet(a); err == nil {
			nets = append(nets, p)
		}
	}
	slices.SortFunc(nets, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return slices.Compact(nets)
}

// Admits tells whether a client address may reach the NAS. The NAS
// itself (loopback) always may.
func Admits(f store.FirewallSettings, local []netip.Prefix, ip string) bool {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	a = a.Unmap().WithZone("")
	if !f.Enabled || a.IsLoopback() {
		return true
	}
	for _, p := range Allowed(f, local) {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Render writes the nftables script: it replaces the nasd table in one
// transaction (the first line makes sure there is a table to delete).
func Render(f store.FirewallSettings, local []netip.Prefix) string {
	var v4, v6 []string
	for _, p := range Allowed(f, local) {
		if p.Addr().Is4() {
			v4 = append(v4, p.String())
		} else {
			v6 = append(v6, p.String())
		}
	}
	set := func(name, typ string, elems []string) string {
		s := "\tset " + name + " {\n\t\ttype " + typ + "\n\t\tflags interval\n\t\tauto-merge\n"
		if len(elems) > 0 {
			s += "\t\telements = { " + strings.Join(elems, ", ") + " }\n"
		}
		return s + "\t}\n"
	}
	var b strings.Builder
	b.WriteString("# Generated by nasd (System > Firewall). Do not edit: changes are overwritten.\n")
	b.WriteString("table inet " + table + "\ndelete table inet " + table + "\n")
	b.WriteString("table inet " + table + " {\n")
	b.WriteString(set("allowed4", "ipv4_addr", v4))
	b.WriteString(set("allowed6", "ipv6_addr", v6))
	b.WriteString("\tcounter blocked {\n\t}\n")
	b.WriteString(`	chain input {
		type filter hook input priority filter; policy drop;
		iif "lo" accept
		ct state established,related accept
		ct state invalid drop
		meta l4proto { icmp, ipv6-icmp } accept
		udp sport 67 udp dport 68 accept
		ip6 saddr fe80::/10 udp sport 547 udp dport 546 accept
		ip saddr @allowed4 accept
		ip6 saddr @allowed6 accept
		limit rate 10/minute burst 5 packets log prefix "` + logPrefix + `" level info
		counter name "blocked" drop
	}
}
`)
	return b.String()
}

// ------------------------------------------------------------ applying

// Installed tells whether the nft tool is there.
func (s *Service) Installed() bool {
	_, err := os.Stat(s.NftPath)
	return err == nil
}

func (s *Service) install(ctx context.Context) error {
	if s.Installed() {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := s.RunEnv(ctx, []string{"DEBIAN_FRONTEND=noninteractive"}, "apt-get", "install", "-y", "--no-install-recommends", "nftables"); err != nil {
		return fmt.Errorf("installing nftables: %w", err)
	}
	return nil
}

// load puts rules in the kernel now ("" removes them).
func (s *Service) load(ctx context.Context, rules string) error {
	if rules == "" {
		if s.Installed() {
			s.Run(ctx, "nft", "delete", "table", "inet", table) // fails when there is none
		}
		s.applied = ""
		return nil
	}
	tmp, err := os.CreateTemp("", "nasd-firewall-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(rules)
	tmp.Close()
	if _, err := s.Run(ctx, "nft", "-f", tmp.Name()); err != nil {
		return err
	}
	s.applied = rules
	return nil
}

const unit = `# Generated by nasd: loads the firewall kept in System > Firewall.
[Unit]
Description=nasd firewall
DefaultDependencies=no
Before=network-pre.target shutdown.target
Wants=network-pre.target
Conflicts=shutdown.target

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/usr/sbin/nft -f /etc/nasd/firewall.nft

[Install]
WantedBy=sysinit.target
`

// persist makes rules the ones loaded at boot ("" = none).
func (s *Service) persist(ctx context.Context, rules string) error {
	if rules == "" {
		if _, err := os.Stat(s.UnitFile); err == nil {
			s.Run(ctx, "systemctl", "disable", "--quiet", filepath.Base(s.UnitFile))
			os.Remove(s.UnitFile)
			s.Run(ctx, "systemctl", "daemon-reload")
		}
		os.Remove(s.RulesFile)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.RulesFile), 0o755); err != nil {
		return err
	}
	if err := writeFile(s.RulesFile, rules); err != nil {
		return err
	}
	if old, _ := os.ReadFile(s.UnitFile); string(old) != unit {
		if err := writeFile(s.UnitFile, unit); err != nil {
			return err
		}
		s.Run(ctx, "systemctl", "daemon-reload")
	}
	_, err := s.Run(ctx, "systemctl", "enable", "--quiet", filepath.Base(s.UnitFile))
	return err
}

func writeFile(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Service) rules(f store.FirewallSettings) string {
	if !f.Enabled {
		return ""
	}
	return Render(f, s.LocalNets())
}

// Start is called when nasd starts: the rules in the kernel are made to
// match the settings, and a change that was never kept still rolls back.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Store.Read()
	if st.FirewallPending != nil {
		s.armRollback(max(st.FirewallPending.Deadline.Sub(s.Now()), time.Second))
	}
	if !st.Firewall.Enabled && !s.Installed() {
		return nil
	}
	want := s.rules(st.Firewall)
	// Loaded at boot already (nasd-firewall.service): leave them, so the
	// blocked counter keeps counting from boot.
	if boot, _ := os.ReadFile(s.RulesFile); want != "" && string(boot) == want && st.FirewallPending == nil {
		if _, err := s.Run(ctx, "nft", "list", "table", "inet", table); err == nil {
			s.applied = want
			return nil
		}
	}
	if err := s.load(ctx, want); err != nil {
		return err
	}
	if st.FirewallPending == nil {
		return s.persist(ctx, s.rules(st.Firewall))
	}
	return nil
}

// Watch follows the NAS's own networks (scope "local"): when they change
// (another address from the router), the rules are rewritten.
func (s *Service) Watch(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.mu.Lock()
		st := s.Store.Read()
		if want := s.rules(st.Firewall); st.Firewall.Enabled && want != s.applied {
			if err := s.load(ctx, want); err == nil && st.FirewallPending == nil {
				s.persist(ctx, want)
			}
		}
		s.mu.Unlock()
	}
}

// ErrLockout: the change would block the computer making it.
var ErrLockout = errors.New("this would block your own computer")

// Set applies new settings and starts the confirmation countdown. client
// is the address of the computer making the change: it must stay allowed.
func (s *Service) Set(ctx context.Context, f store.FirewallSettings, client string) (time.Time, error) {
	if err := Normalize(&f); err != nil {
		return time.Time{}, err
	}
	if !Admits(f, s.LocalNets(), client) {
		return time.Time{}, fmt.Errorf("%w (%s): add it to the allowed networks first", ErrLockout, client)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Store.Read()
	if st.FirewallPending != nil {
		return time.Time{}, errors.New("a firewall change is waiting to be kept or undone")
	}
	if f.Enabled {
		if err := s.install(ctx); err != nil {
			return time.Time{}, err
		}
	}
	previous := st.Firewall
	if err := s.load(ctx, s.rules(f)); err != nil {
		s.load(ctx, s.rules(previous))
		return time.Time{}, err
	}
	// Switching off is always safe: nothing to confirm.
	if !f.Enabled {
		err := s.Store.Update(func(st *store.State) error { st.Firewall = f; return nil })
		if err == nil {
			err = s.persist(ctx, "")
		}
		return time.Time{}, err
	}
	deadline := s.Now().Add(s.ConfirmWindow)
	err := s.Store.Update(func(st *store.State) error {
		st.Firewall = f
		st.FirewallPending = &store.FirewallChange{Previous: previous, Deadline: deadline}
		return nil
	})
	s.armRollback(s.ConfirmWindow)
	return deadline, err
}

// Keep confirms the pending change: it becomes the one loaded at boot.
func (s *Service) Keep(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.Store.Read()
	if st.FirewallPending == nil {
		return errors.New("nothing to keep")
	}
	if s.rollback != nil {
		s.rollback.Stop()
	}
	if err := s.Store.Update(func(st *store.State) error { st.FirewallPending = nil; return nil }); err != nil {
		return err
	}
	return s.persist(ctx, s.rules(st.Firewall))
}

// Undo puts the previous settings back now.
func (s *Service) Undo(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.undo(ctx, false)
}

func (s *Service) armRollback(after time.Duration) {
	if s.rollback != nil {
		s.rollback.Stop()
	}
	s.rollback = time.AfterFunc(after, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.undo(context.Background(), true)
	})
}

func (s *Service) undo(ctx context.Context, timedOut bool) error {
	if s.rollback != nil {
		s.rollback.Stop()
	}
	st := s.Store.Read()
	if st.FirewallPending == nil {
		return errors.New("nothing to undo")
	}
	prev := st.FirewallPending.Previous
	err := s.load(ctx, s.rules(prev))
	s.Store.Update(func(st *store.State) error {
		st.Firewall, st.FirewallPending = prev, nil
		return nil
	})
	if timedOut {
		s.Notify("firewall", "Firewall change undone",
			"The new firewall settings were not kept in time, so the previous ones were restored.")
	}
	return err
}

// TurnOff switches the firewall off at once (the Reset button at the back).
func (s *Service) TurnOff(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rollback != nil {
		s.rollback.Stop()
	}
	if err := s.Store.Update(func(st *store.State) error {
		st.Firewall.Enabled, st.FirewallPending = false, nil
		return nil
	}); err != nil {
		return err
	}
	s.load(ctx, "")
	return s.persist(ctx, "")
}

// ------------------------------------------------------------ status

// Status is what the Firewall page shows.
type Status struct {
	Settings  store.FirewallSettings `json:"settings"`
	Pending   *store.FirewallChange  `json:"pending"`
	Installed bool                   `json:"installed"`
	Active    bool                   `json:"active"`  // rules loaded in the kernel
	Local     []string               `json:"local"`   // the NAS's own networks
	Private   []string               `json:"private"` // what "private" means
	Client    string                 `json:"client"`  // the computer looking at the page
	// ClientAllowed: whether that computer would still be let in with the
	// firewall on and the current scope and networks.
	ClientAllowed bool      `json:"clientAllowed"`
	Blocked       int64     `json:"blocked"` // packets dropped since the rules were loaded
	Recent        []Attempt `json:"recent"`  // last logged blocked attempts, newest first
	Ports         []Port    `json:"ports"`
}

type Attempt struct {
	Time   time.Time `json:"time"`
	Source string    `json:"source"`
	Proto  string    `json:"proto"`
	Port   int       `json:"port"`
}

func (s *Service) Status(ctx context.Context, client string) Status {
	st := s.Store.Read()
	local := s.LocalNets()
	out := Status{Settings: st.Firewall, Pending: st.FirewallPending, Installed: s.Installed(),
		Private: privateNets, Local: []string{}, Recent: []Attempt{}, Ports: []Port{}}
	if out.Settings.Allow == nil {
		out.Settings.Allow = []string{}
	}
	if out.Settings.Scope == "" {
		out.Settings.Scope = "private"
	}
	for _, p := range local {
		out.Local = append(out.Local, p.Masked().String())
	}
	if a, err := netip.ParseAddr(client); err == nil {
		client = a.Unmap().WithZone("").String()
	}
	on := st.Firewall
	on.Enabled = true
	out.Client, out.ClientAllowed = client, Admits(on, local, client)
	if out.Installed {
		if data, err := s.Run(ctx, "nft", "-j", "list", "counter", "inet", table, "blocked"); err == nil {
			out.Active = true
			out.Blocked = parseCounter(data)
		}
	}
	if data, err := s.Run(ctx, "journalctl", "-k", "-b", "--no-pager", "-o", "short-unix", "-g", logPrefix, "-n", "30"); err == nil {
		out.Recent = parseAttempts(data)
	}
	if data, err := s.Run(ctx, "ss", "-tulnpH"); err == nil {
		out.Ports = parsePorts(data)
	}
	return out
}

func parseCounter(data []byte) int64 {
	var v struct {
		Nftables []struct {
			Counter *struct {
				Packets int64 `json:"packets"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	json.Unmarshal(data, &v)
	for _, n := range v.Nftables {
		if n.Counter != nil {
			return n.Counter.Packets
		}
	}
	return 0
}

// parseAttempts reads kernel log lines like
// "1731280801.123456 nas kernel: nasd-firewall: IN=enp2s0 ... SRC=1.2.3.4 DST=... PROTO=TCP SPT=5 DPT=22 ...".
func parseAttempts(data []byte) []Attempt {
	out := []Attempt{}
	for _, l := range strings.Split(string(data), "\n") {
		i := strings.Index(l, logPrefix)
		if i < 0 {
			continue
		}
		var a Attempt
		if sec, err := strconv.ParseFloat(strings.Fields(l)[0], 64); err == nil {
			a.Time = time.Unix(int64(sec), 0)
		}
		for _, f := range strings.Fields(l[i+len(logPrefix):]) {
			k, v, _ := strings.Cut(f, "=")
			switch k {
			case "SRC":
				a.Source = v
			case "PROTO":
				a.Proto = strings.ToLower(v)
			case "DPT":
				a.Port, _ = strconv.Atoi(v)
			}
		}
		if a.Source != "" {
			out = append(out, a)
		}
	}
	slices.Reverse(out)
	return out
}

// Port is a service listening on the network.
type Port struct {
	Proto   string `json:"proto"` // tcp, udp
	Port    int    `json:"port"`
	Process string `json:"process"`
	Local   bool   `json:"local"` // only on the NAS itself (loopback)
}

// parsePorts reads "ss -tulnpH": one line per socket; IPv4 and IPv6 of
// the same service are merged.
func parsePorts(data []byte) []Port {
	type key struct {
		proto string
		port  int
		proc  string
	}
	seen := map[key]int{} // index in out
	var out []Port
	for _, l := range strings.Split(string(data), "\n") {
		f := strings.Fields(l)
		if len(f) < 5 {
			continue
		}
		addr := f[4]
		j := strings.LastIndex(addr, ":")
		if j < 0 {
			continue
		}
		port, err := strconv.Atoi(addr[j+1:])
		if err != nil {
			continue
		}
		host, _, _ := strings.Cut(strings.Trim(addr[:j], "[]"), "%")
		proc := ""
		if len(f) > 6 {
			if a := strings.Index(f[6], `(("`); a >= 0 {
				proc, _, _ = strings.Cut(f[6][a+3:], `"`)
			}
		}
		a, err := netip.ParseAddr(host)
		loopback := err == nil && a.IsLoopback()
		k := key{f[0], port, proc}
		if i, ok := seen[k]; ok {
			out[i].Local = out[i].Local && loopback
			continue
		}
		seen[k] = len(out)
		out = append(out, Port{Proto: f[0], Port: port, Process: proc, Local: loopback})
	}
	slices.SortFunc(out, func(a, b Port) int {
		if a.Port != b.Port {
			return a.Port - b.Port
		}
		return strings.Compare(a.Proto, b.Proto)
	})
	return out
}

// localNets: the prefixes of the NAS's global addresses.
func localNets() []netip.Prefix {
	data, err := run.Exec(context.Background(), "ip", "-j", "addr", "show")
	if err != nil {
		return nil
	}
	return parseAddrs(data)
}

func parseAddrs(data []byte) []netip.Prefix {
	var links []struct {
		Ifname   string `json:"ifname"`
		AddrInfo []struct {
			Local     string `json:"local"`
			Prefixlen int    `json:"prefixlen"`
			Scope     string `json:"scope"`
		} `json:"addr_info"`
	}
	json.Unmarshal(data, &links)
	var out []netip.Prefix
	for _, l := range links {
		for _, a := range l.AddrInfo {
			addr, err := netip.ParseAddr(a.Local)
			if err != nil || a.Scope != "global" || addr.IsLoopback() {
				continue
			}
			if p, err := addr.Prefix(a.Prefixlen); err == nil && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}
