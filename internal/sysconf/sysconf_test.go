package sysconf

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
)

type fake struct {
	mu   sync.Mutex
	cmds []string
}

func (f *fake) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	switch {
	case name == "timedatectl" && args[0] == "list-timezones":
		return []byte("Europe/Brussels\nUTC\n"), nil
	case name == "timedatectl" && args[0] == "show":
		return []byte("Timezone=UTC\nNTP=yes\nNTPSynchronized=yes\n"), nil
	case name == "apt":
		return []byte("Listing...\nlibc6/stable-security 2.41-12+deb13u2 amd64 [upgradable from: 2.41-12+deb13u1]\nopenssl/stable 3.5.4-1 amd64 [upgradable from: 3.5.1-1]\n"), nil
	}
	return nil, nil
}

func (f *fake) all() string { f.mu.Lock(); defer f.mu.Unlock(); return strings.Join(f.cmds, "\n") }

func newTest(t *testing.T) (*Service, *fake) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "state.json"))
	f := &fake{}
	s := NewService(st, jobs.NewManager(f.run))
	s.Run = f.run
	s.NetworkDir = filepath.Join(dir, "network")
	s.Hosts = filepath.Join(dir, "hosts")
	s.Timesyncd = filepath.Join(dir, "timesyncd", "nasd.conf")
	s.Sys = filepath.Join(dir, "sys")
	os.MkdirAll(s.NetworkDir, 0o755)
	for _, nic := range []string{"eth0", "eth1"} {
		os.MkdirAll(filepath.Join(s.Sys, "class", "net", nic, "device"), 0o755)
	}
	os.MkdirAll(filepath.Join(s.Sys, "class", "net", "lo"), 0o755)
	restarts := 0
	s.RestartSelf = func() { restarts++ }
	return s, f
}

func TestRenderNetwork(t *testing.T) {
	static := renderNetwork(store.NetworkSettings{Managed: true, Interfaces: []string{"eth0"}, Address: "192.168.1.50/24",
		Gateway: "192.168.1.1", DNS: []string{"1.1.1.1"}, MTU: 9000})
	f := static["05-nasd-eth0.network"]
	for _, want := range []string{"Name=eth0", "MTUBytes=9000", "Address=192.168.1.50/24", "Gateway=192.168.1.1", "DNS=1.1.1.1"} {
		if !strings.Contains(f, want) {
			t.Errorf("missing %q in:\n%s", want, f)
		}
	}
	bond := renderNetwork(store.NetworkSettings{Managed: true, Interfaces: []string{"eth0", "eth1"}, Bond: "802.3ad", DHCP: true})
	if len(bond) != 4 || !strings.Contains(bond["05-nasd-bond0.netdev"], "Mode=802.3ad") ||
		!strings.Contains(bond["05-nasd-eth1.network"], "Bond=bond0") || !strings.Contains(bond["05-nasd-bond0.network"], "DHCP=ipv4") {
		t.Errorf("bond files: %v", bond)
	}
	if len(renderNetwork(store.NetworkSettings{})) != 0 {
		t.Error("unmanaged network = no files")
	}
}

func TestValidateNetwork(t *testing.T) {
	s, _ := newTest(t)
	for label, n := range map[string]store.NetworkSettings{
		"no interface":     {Managed: true, DHCP: true},
		"unknown iface":    {Managed: true, Interfaces: []string{"wlan0"}, DHCP: true},
		"lo is not a NIC":  {Managed: true, Interfaces: []string{"lo"}, DHCP: true},
		"two without bond": {Managed: true, Interfaces: []string{"eth0", "eth1"}, DHCP: true},
		"bad address":      {Managed: true, Interfaces: []string{"eth0"}, Address: "192.168.1.50"},
		"gateway outside":  {Managed: true, Interfaces: []string{"eth0"}, Address: "192.168.1.50/24", Gateway: "10.0.0.1"},
		"bad dns":          {Managed: true, Interfaces: []string{"eth0"}, Address: "192.168.1.50/24", DNS: []string{"dns.google"}},
		"mtu":              {Managed: true, Interfaces: []string{"eth0"}, DHCP: true, MTU: 10000},
	} {
		if err := s.validateNetwork(&n); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func TestNetworkRollsBackWithoutConfirmation(t *testing.T) {
	s, f := newTest(t)
	s.ConfirmWindow = 100 * time.Millisecond
	var mu sync.Mutex
	var notes []string
	s.Notify = func(k, subj, b string) { mu.Lock(); notes = append(notes, subj); mu.Unlock() }
	ctx := context.Background()

	// 1. A confirmed change stays.
	if _, err := s.SetNetwork(ctx, store.NetworkSettings{Managed: true, Interfaces: []string{"eth0"}, DHCP: true, MTU: 1400}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetNetwork(ctx, store.NetworkSettings{Managed: true, Interfaces: []string{"eth0"}, DHCP: true}); err == nil {
		t.Error("a second change must wait for the first to be confirmed")
	}
	if err := s.ConfirmNetwork(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if st := s.Store.Read(); st.Network.MTU != 1400 || st.NetworkPending != nil {
		t.Fatalf("confirmed change lost: %+v", st.Network)
	}

	// 2. An unconfirmed change is undone.
	if _, err := s.SetNetwork(ctx, store.NetworkSettings{Managed: true, Interfaces: []string{"eth0", "eth1"}, Bond: "active-backup",
		Address: "10.9.9.9/24"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.NetworkDir, "05-nasd-bond0.netdev")); err != nil {
		t.Fatal("bond files not written")
	}
	time.Sleep(300 * time.Millisecond)
	st := s.Store.Read()
	if st.NetworkPending != nil || st.Network.MTU != 1400 || st.Network.Bond != "" {
		t.Fatalf("not rolled back: %+v", st.Network)
	}
	if _, err := os.Stat(filepath.Join(s.NetworkDir, "05-nasd-bond0.netdev")); err == nil {
		t.Error("bond files must be removed by the rollback")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(f.all(), "ip link set dev eth0 mtu 1500") {
		t.Error("going back to a config without MTU must reset it to 1500")
	}
	if !strings.Contains(f.all(), "ip link delete bond0") || len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "rolled back") {
		t.Errorf("rollback incomplete: %v\n%s", notes, f.all())
	}
}

func TestHostnameAndTime(t *testing.T) {
	s, f := newTest(t)
	ctx := context.Background()
	os.WriteFile(s.Hosts, []byte("127.0.0.1\tlocalhost\n127.0.1.1\told-name\n"), 0o644)
	for _, bad := range []string{"", "-nas", "my_nas", "a-name-that-is-too-long"} {
		if err := s.SetHostname(ctx, bad); err == nil {
			t.Errorf("hostname %q must be refused", bad)
		}
	}
	if err := s.SetHostname(ctx, "Attic-NAS"); err != nil {
		t.Fatal(err)
	}
	hosts, _ := os.ReadFile(s.Hosts)
	if !strings.Contains(string(hosts), "127.0.1.1\tattic-nas") || strings.Contains(string(hosts), "old-name") {
		t.Errorf("hosts:\n%s", hosts)
	}

	if err := s.SetTime(ctx, TimeRequest{Timezone: "Mars/Olympus", NTP: true}); err == nil {
		t.Error("unknown time zone accepted")
	}
	if err := s.SetTime(ctx, TimeRequest{Timezone: "Europe/Brussels", NTP: true, NTPServers: []string{"be.pool.ntp.org"}}); err != nil {
		t.Fatal(err)
	}
	conf, _ := os.ReadFile(s.Timesyncd)
	if !strings.Contains(string(conf), "NTP=be.pool.ntp.org") || !strings.Contains(f.all(), "timedatectl set-timezone Europe/Brussels") {
		t.Errorf("timesyncd: %s\n%s", conf, f.all())
	}
}

func TestUpdatesParsing(t *testing.T) {
	s, _ := newTest(t)
	u, err := s.Updates(context.Background())
	if err != nil || len(u.Updates) != 2 || u.Updates[0] != (Update{Package: "libc6", From: "2.41-12+deb13u1", To: "2.41-12+deb13u2"}) {
		t.Errorf("%v %+v", err, u)
	}
}
