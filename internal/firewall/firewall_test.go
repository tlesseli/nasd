package firewall

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readynas/internal/store"
)

func TestNormalize(t *testing.T) {
	f := store.FirewallSettings{Allow: []string{" 192.168.68.7 ", "192.168.68.9/22", "", "192.168.68.0/22", "fd00::1"}}
	if err := Normalize(&f); err != nil {
		t.Fatal(err)
	}
	if f.Scope != "private" || strings.Join(f.Allow, " ") != "192.168.68.7/32 192.168.68.0/22 fd00::1/128" {
		t.Errorf("%+v", f)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "nas.local", "300.1.1.1"} {
		if err := Normalize(&store.FirewallSettings{Allow: []string{bad}}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := Normalize(&store.FirewallSettings{Scope: "all"}); err == nil {
		t.Error("unknown scope accepted")
	}
}

func TestAdmits(t *testing.T) {
	local := []netip.Prefix{netip.MustParsePrefix("192.168.0.6/24")}
	priv := store.FirewallSettings{Enabled: true, Scope: "private"}
	loc := store.FirewallSettings{Enabled: true, Scope: "local", Allow: []string{"100.64.0.0/10"}}
	for _, c := range []struct {
		f    store.FirewallSettings
		ip   string
		want bool
	}{
		{priv, "192.168.68.20", true},
		{priv, "::ffff:10.1.2.3", true},
		{priv, "fe80::1%enp2s0", true},
		{priv, "8.8.8.8", false},
		{priv, "2a02:1810::1", false},
		{priv, "127.0.0.1", true},
		{loc, "192.168.0.5", true},
		{loc, "192.168.68.20", false},
		{loc, "100.100.1.1", true},
		{store.FirewallSettings{}, "8.8.8.8", true}, // off
	} {
		if got := Admits(c.f, local, c.ip); got != c.want {
			t.Errorf("%s %s: %v", c.f.Scope, c.ip, got)
		}
	}
}

func TestRender(t *testing.T) {
	r := Render(store.FirewallSettings{Enabled: true, Scope: "local", Allow: []string{"192.168.68.0/22"}},
		[]netip.Prefix{netip.MustParsePrefix("192.168.0.6/24"), netip.MustParsePrefix("2a02:a03f:1::5/64")})
	for _, want := range []string{
		"table inet nasd\ndelete table inet nasd\ntable inet nasd {",
		"elements = { 192.168.0.0/24, 192.168.68.0/22 }",
		"elements = { 2a02:a03f:1::/64, fe80::/10 }",
		"policy drop;",
		`counter name "blocked" drop`,
	} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q in:\n%s", want, r)
		}
	}
	// No IPv6 network: an empty set, no "elements".
	r = Render(store.FirewallSettings{Enabled: true, Scope: "local"}, []netip.Prefix{netip.MustParsePrefix("10.0.0.2/8")})
	if strings.Count(r, "elements") != 2 { // fe80::/10 is always there
		t.Errorf("%s", r)
	}
}

func TestParsers(t *testing.T) {
	ports := parsePorts([]byte(`udp UNCONN 0 0 127.0.0.53%lo:53 0.0.0.0:* users:(("systemd-resolve",pid=355,fd=18))
udp UNCONN 0 0 192.168.0.6%enp2s0:68 0.0.0.0:* users:(("systemd-network",pid=392,fd=33))
tcp LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:* users:(("systemd-resolve",pid=355,fd=19))
tcp LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=743,fd=6))
tcp LISTEN 0 4096 *:8443 *:* users:(("nasd",pid=325002,fd=8))
tcp LISTEN 0 128 [::]:22 [::]:* users:(("sshd",pid=743,fd=7))
`))
	if len(ports) != 5 || ports[0].Port != 22 || ports[0].Process != "sshd" || ports[0].Local ||
		!ports[1].Local || ports[1].Proto != "tcp" || ports[4].Port != 8443 {
		t.Errorf("%+v", ports)
	}
	att := parseAttempts([]byte("1731280801.123456 nas kernel: nasd-firewall: IN=enp2s0 OUT= SRC=203.0.113.9 DST=192.168.0.6 LEN=60 PROTO=TCP SPT=40000 DPT=22 SYN\n" +
		"1731280901.5 nas kernel: nasd-firewall: IN=enp2s0 OUT= SRC=2001:db8::1 DST=2a02::5 PROTO=UDP SPT=1 DPT=445\n"))
	if len(att) != 2 || att[0].Source != "2001:db8::1" || att[1].Port != 22 || att[1].Proto != "tcp" || att[1].Time.Unix() != 1731280801 {
		t.Errorf("%+v", att)
	}
	if n := parseCounter([]byte(`{"nftables":[{"metainfo":{}},{"counter":{"family":"inet","name":"blocked","table":"nasd","packets":42,"bytes":2520}}]}`)); n != 42 {
		t.Errorf("counter %d", n)
	}
	nets := parseAddrs([]byte(`[{"ifname":"lo","addr_info":[{"local":"127.0.0.1","prefixlen":8,"scope":"host"}]},
{"ifname":"enp2s0","addr_info":[{"local":"192.168.0.6","prefixlen":24,"scope":"global"},{"local":"fe80::1","prefixlen":64,"scope":"link"}]}]`))
	if len(nets) != 1 || nets[0].Masked().String() != "192.168.0.0/24" {
		t.Errorf("%v", nets)
	}
}

type fake struct{ cmds []string }

func (f *fake) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	return nil, nil
}

func newTest(t *testing.T) (*Service, *fake) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "state.json"))
	f := &fake{}
	s := NewService(st)
	s.Run = f.run
	s.RunEnv = func(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
		return f.run(ctx, name, args...)
	}
	s.RulesFile = filepath.Join(dir, "etc", "firewall.nft")
	s.UnitFile = filepath.Join(dir, "nasd-firewall.service")
	s.NftPath = filepath.Join(dir, "nft")
	os.WriteFile(s.NftPath, nil, 0o755)
	s.LocalNets = func() []netip.Prefix { return []netip.Prefix{netip.MustParsePrefix("192.168.0.6/24")} }
	s.ConfirmWindow = time.Hour
	return s, f
}

func TestSetKeepUndo(t *testing.T) {
	s, f := newTest(t)
	ctx := context.Background()

	// A change that would block the computer making it is refused.
	_, err := s.Set(ctx, store.FirewallSettings{Enabled: true, Scope: "local"}, "192.168.68.20")
	if err == nil || !strings.Contains(err.Error(), "block your own computer") {
		t.Fatalf("lockout not refused: %v", err)
	}

	// On, then kept: the rules go to the boot file and the unit is enabled.
	if _, err := s.Set(ctx, store.FirewallSettings{Enabled: true}, "192.168.68.20"); err != nil {
		t.Fatal(err)
	}
	if st := s.Store.Read(); !st.Firewall.Enabled || st.FirewallPending == nil {
		t.Fatalf("not pending: %+v", st.Firewall)
	}
	if _, err := os.Stat(s.RulesFile); err == nil {
		t.Error("an unconfirmed change was written for the next boot")
	}
	if err := s.Keep(ctx); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(s.RulesFile); !strings.Contains(string(data), "192.168.0.0/16") {
		t.Errorf("boot rules: %s", data)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "systemctl enable --quiet nasd-firewall.service") {
		t.Errorf("unit not enabled: %v", f.cmds)
	}

	// A second change, undone: the first comes back.
	if _, err := s.Set(ctx, store.FirewallSettings{Enabled: true, Scope: "local", Allow: []string{"192.168.68.0/22"}}, "192.168.68.20"); err != nil {
		t.Fatal(err)
	}
	if err := s.Undo(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Store.Read(); st.Firewall.Scope != "private" || st.FirewallPending != nil {
		t.Errorf("after undo: %+v %+v", st.Firewall, st.FirewallPending)
	}

	// Not kept in time: rolled back by itself.
	s.ConfirmWindow = 10 * time.Millisecond
	notified := make(chan string, 1)
	s.Notify = func(kind, subject, body string) { notified <- kind }
	if _, err := s.Set(ctx, store.FirewallSettings{Enabled: true, Scope: "local"}, "192.168.0.5"); err != nil {
		t.Fatal(err)
	}
	select {
	case k := <-notified:
		if k != "firewall" {
			t.Errorf("kind %s", k)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no rollback")
	}
	if st := s.Store.Read(); st.Firewall.Scope != "private" {
		t.Errorf("not rolled back: %+v", st.Firewall)
	}

	// Off: nothing to confirm, nothing loaded at boot.
	if _, err := s.Set(ctx, store.FirewallSettings{}, "8.8.8.8"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.RulesFile); err == nil {
		t.Error("boot rules left behind")
	}
	if st := s.Store.Read(); st.Firewall.Enabled || st.FirewallPending != nil {
		t.Errorf("%+v", st)
	}
}

func TestTurnOff(t *testing.T) {
	s, f := newTest(t)
	ctx := context.Background()
	s.Set(ctx, store.FirewallSettings{Enabled: true}, "192.168.0.5")
	if err := s.TurnOff(ctx); err != nil {
		t.Fatal(err)
	}
	if st := s.Store.Read(); st.Firewall.Enabled || st.FirewallPending != nil {
		t.Errorf("%+v", st.Firewall)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "nft delete table inet nasd") {
		t.Errorf("rules not removed: %v", f.cmds)
	}
}
