package shares

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"readynas/internal/store"
)

type fakeSystem struct {
	cmds   []string
	stdins []string
	fail   map[string]error // command prefix -> error
}

func (f *fakeSystem) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	for prefix, err := range f.fail {
		if strings.HasPrefix(cmd, prefix) {
			return nil, err
		}
	}
	if strings.HasPrefix(cmd, "systemctl is-active") {
		return []byte("active\n"), nil
	}
	return nil, nil
}

func (f *fakeSystem) runInput(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	f.stdins = append(f.stdins, stdin)
	return f.run(ctx, name, args...)
}

func newTestService(t *testing.T) (*Service, *fakeSystem, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.Update(func(s *store.State) error {
		s.Users = append(s.Users, store.User{Name: "admin", Admin: true})
		return nil
	})
	vol := filepath.Join(dir, "data", "media")
	os.MkdirAll(vol, 0o755)
	os.MkdirAll(filepath.Join(dir, "samba"), 0o755)
	f := &fakeSystem{fail: map[string]error{}}
	s := &Service{
		Store: st, Run: f.run, RunInput: f.runInput,
		SmbConf:   filepath.Join(dir, "samba", "smb.conf"),
		RsyncConf: filepath.Join(dir, "rsyncd.conf"),
		AvahiDir:  filepath.Join(dir, "avahi"),
		Mounted: func(context.Context) (map[string]string, error) {
			return map[string]string{"media": vol}, nil
		},
		Chown:       func(string, int, int) error { return nil },
		GroupID:     func(string) (int, error) { return 1001, nil },
		UserID:      func(string) (int, error) { return 1002, nil },
		QuotaMounts: func(context.Context) ([]string, error) { return []string{vol}, nil },
	}
	return s, f, vol
}

func TestCreateUser(t *testing.T) {
	s, f, _ := newTestService(t)
	ctx := context.Background()
	if err := s.CreateUser(ctx, "alice", "secret-password"); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.cmds, "\n")
	if !strings.Contains(all, "useradd --no-create-home --no-user-group --gid nasusers --home-dir /nonexistent --shell /usr/sbin/nologin") {
		t.Errorf("useradd missing:\n%s", all)
	}
	if strings.Contains(all, "secret-password") {
		t.Error("the password must never appear in command arguments")
	}
	if len(f.stdins) != 1 || f.stdins[0] != "secret-password\nsecret-password\n" {
		t.Errorf("password must go to smbpasswd's stdin twice: %q", f.stdins)
	}

	for _, bad := range []struct{ name, pw string }{
		{"alice", "secret-password"}, // exists
		{"Alice", "secret-password"}, // uppercase
		{"admin", "secret-password"}, // reserved
		{"root", "secret-password"},  // reserved
		{"bob", "short"},             // short password
	} {
		if err := s.CreateUser(ctx, bad.name, bad.pw); err == nil {
			t.Errorf("CreateUser(%q) must fail", bad.name)
		}
	}
}

func TestCreateUserRollsBack(t *testing.T) {
	s, f, _ := newTestService(t)
	f.fail["smbpasswd"] = os.ErrPermission
	if err := s.CreateUser(context.Background(), "carol", "secret-password"); err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "userdel carol") {
		t.Error("the Linux account must be removed when smbpasswd fails")
	}
	if st := s.Store.Read(); st.FindUser("carol") != nil {
		t.Error("the user must not be saved")
	}
}

func TestShareLifecycle(t *testing.T) {
	s, f, vol := newTestService(t)
	ctx := context.Background()
	s.CreateUser(ctx, "alice", "secret-password")
	s.CreateUser(ctx, "bob", "secret-password")

	err := s.CreateShare(ctx, ShareRequest{Name: "Photos", Volume: "media", Description: "Family photos",
		Access: map[string]string{"alice": "rw", "bob": "ro"}, RecycleBin: true})
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(vol, "Photos")); err != nil || fi.Mode()&os.ModeSetgid == 0 {
		t.Errorf("share folder must exist with setgid: %v %v", fi, err)
	}
	conf, _ := os.ReadFile(s.SmbConf)
	for _, want := range []string{
		"server min protocol = SMB2_10",
		"[Photos]",
		"path = " + filepath.Join(vol, "Photos"),
		"write list = alice",
		"valid users = alice bob",
		"vfs objects = fruit streams_xattr recycle",
	} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("smb.conf lacks %q:\n%s", want, conf)
		}
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "testparm --suppress-prompt") {
		t.Error("config must be validated with testparm")
	}

	// Errors
	for label, req := range map[string]ShareRequest{
		"duplicate (case-insensitive)": {Name: "photos", Volume: "media"},
		"reserved":                     {Name: "homes", Volume: "media"},
		"bad name":                     {Name: "a/b", Volume: "media"},
		"unknown volume":               {Name: "x", Volume: "nope"},
		"unknown user":                 {Name: "x", Volume: "media", Access: map[string]string{"zed": "rw"}},
		"bad access":                   {Name: "x", Volume: "media", Access: map[string]string{"alice": "admin"}},
		"admin is not a share user":    {Name: "x", Volume: "media", Access: map[string]string{"admin": "rw"}},
	} {
		if err := s.CreateShare(ctx, req); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}

	// Deleting a user removes them from shares.
	if err := s.DeleteUser(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	st := s.Store.Read()
	if acc := st.FindShare("Photos").Access; len(acc) != 1 || acc["alice"] != "rw" {
		t.Errorf("bob should be gone from the share: %v", acc)
	}

	// A failing testparm must not replace the config.
	before, _ := os.ReadFile(s.SmbConf)
	f.fail["testparm"] = os.ErrInvalid
	if err := s.UpdateShare(ctx, "Photos", ShareRequest{Description: "changed"}); err == nil {
		t.Error("expected testparm failure to be reported")
	}
	if after, _ := os.ReadFile(s.SmbConf); string(after) != string(before) {
		t.Error("an invalid config must never be installed")
	}
	delete(f.fail, "testparm")

	if err := s.DeleteShare(ctx, "photos"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(vol, "Photos")); err != nil {
		t.Error("deleting a share must keep its files")
	}
}

func TestRenderEdgeCases(t *testing.T) {
	conf := renderSmbConf(store.State{Shares: []store.Share{
		{Name: "public", Path: "/data/v/public", GuestRead: true, Access: map[string]string{"alice": "rw"}},
		{Name: "locked", Path: "/data/v/locked"},
		{Name: "tm", Path: "/data/v/tm", Access: map[string]string{"alice": "rw"}, TimeMachine: true},
	}})
	pub := section(conf, "public")
	if !strings.Contains(pub, "guest ok = yes") || strings.Contains(pub, "valid users") || !strings.Contains(pub, "write list = alice") {
		t.Errorf("guest share:\n%s", pub)
	}
	if locked := section(conf, "locked"); !strings.Contains(locked, "available = no") || strings.Contains(locked, "valid users") {
		t.Errorf("a share without users must be disabled, never open to all:\n%s", locked)
	}
	if !strings.Contains(section(conf, "tm"), "fruit:time machine = yes") {
		t.Error("time machine flag missing")
	}
	avahi := renderAvahi(store.State{Shares: []store.Share{{Name: "tm", TimeMachine: true}}})
	if !strings.Contains(avahi, "dk0=adVN=tm,adVF=0x82") {
		t.Errorf("avahi:\n%s", avahi)
	}
}

// section returns the lines of one [share] block.
func section(conf, name string) string {
	_, after, _ := strings.Cut(conf, "["+name+"]\n")
	block, _, _ := strings.Cut(after, "\n[")
	return block
}

func TestRsyncAndHosts(t *testing.T) {
	s, f, _ := newTestService(t)
	ctx := context.Background()
	s.CreateUser(ctx, "alice", "secret-password")
	if err := s.CreateShare(ctx, ShareRequest{Name: "Docs", Volume: "media", Access: map[string]string{"alice": "rw"},
		RsyncRead: true, AllowedHosts: []string{"192.168.1.20", "10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateShare(ctx, ShareRequest{Name: "Bad", Volume: "media", AllowedHosts: []string{"my-pc"}}); err == nil {
		t.Error("host names must be refused (Samba resolves them unreliably)")
	}
	smb, _ := os.ReadFile(s.SmbConf)
	if !strings.Contains(section(string(smb), "Docs"), "hosts allow = 192.168.1.20 10.0.0.0/8") {
		t.Errorf("smb.conf:\n%s", smb)
	}
	rs, _ := os.ReadFile(s.RsyncConf)
	if !strings.Contains(string(rs), "[Docs]") || !strings.Contains(string(rs), "hosts allow = 192.168.1.20 10.0.0.0/8") {
		t.Errorf("rsyncd.conf:\n%s", rs)
	}
	all := strings.Join(f.cmds, "\n")
	if !strings.Contains(all, "systemctl disable --now rsync") {
		t.Error("rsync must stay off until enabled")
	}

	f.cmds = nil
	if err := s.SetSettings(ctx, store.Settings{SMBDisabled: true, RsyncEnabled: true}); err != nil {
		t.Fatal(err)
	}
	all = strings.Join(f.cmds, "\n")
	for _, want := range []string{"systemctl disable --now smbd nmbd", "systemctl disable --now wsdd2", "systemctl enable --now rsync"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if _, err := os.Stat(filepath.Join(s.AvahiDir, "nasd-smb.service")); err == nil {
		t.Error("the Bonjour announcement must be removed when SMB is off")
	}
}

func TestHomesQuotaHidden(t *testing.T) {
	s, f, vol := newTestService(t)
	ctx := context.Background()
	s.CreateUser(ctx, "alice", "secret-password")
	if err := s.SetHomeShares(ctx, true, "media"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(vol, "homes", "alice"))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("home folder: %v %v", fi, err)
	}
	s.CreateUser(ctx, "bob", "secret-password") // created after: gets a home too
	if _, err := os.Stat(filepath.Join(vol, "homes", "bob")); err != nil {
		t.Error("new users must get a home folder")
	}
	conf, _ := os.ReadFile(s.SmbConf)
	if !strings.Contains(string(conf), "[homes]") || !strings.Contains(string(conf), "path = "+filepath.Join(vol, "homes")+"/%S") ||
		!strings.Contains(string(conf), "valid users = %S") {
		t.Errorf("smb.conf:\n%s", conf)
	}
	if on := s.OnVolume("media"); len(on) == 0 || on[0] != "home folders" {
		t.Errorf("a volume holding home folders can't be deleted: %v", on)
	}

	if err := s.SetQuota(ctx, "alice", 500); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "setquota -u alice 0 512000 0 0 "+vol) {
		t.Errorf("setquota missing:\n%s", strings.Join(f.cmds, "\n"))
	}
	if s.Users()[0].QuotaMB != 500 {
		t.Error("quota not stored")
	}

	s.CreateShare(ctx, ShareRequest{Name: "Secret", Volume: "media", Access: map[string]string{"alice": "rw"}, Hidden: true})
	conf, _ = os.ReadFile(s.SmbConf)
	if !strings.Contains(section(string(conf), "Secret"), "browseable = no") {
		t.Error("hidden share must not be browseable")
	}
	if err := s.SetHomeShares(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	conf, _ = os.ReadFile(s.SmbConf)
	if strings.Contains(string(conf), "[homes]") {
		t.Error("[homes] must disappear when disabled")
	}
	if _, err := os.Stat(filepath.Join(vol, "homes", "alice")); err != nil {
		t.Error("disabling home shares must keep the folders")
	}
}

func TestPerShareServicesAndDiscovery(t *testing.T) {
	s, f, _ := newTestService(t)
	ctx := context.Background()
	s.CreateUser(ctx, "alice", "secret-password")
	if err := s.CreateShare(ctx, ShareRequest{Name: "Sync", Volume: "media", Access: map[string]string{"alice": "ro"},
		SMBDisabled: true, RsyncRead: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateShare(ctx, ShareRequest{Name: "TM", Volume: "media", SMBDisabled: true, TimeMachine: true}); err == nil {
		t.Error("Time Machine without SMB must be refused")
	}
	smb, _ := os.ReadFile(s.SmbConf)
	rs, _ := os.ReadFile(s.RsyncConf)
	if strings.Contains(string(smb), "[Sync]") || !strings.Contains(string(rs), "[Sync]") {
		t.Errorf("rsync-only share: smb.conf\n%s\nrsyncd.conf\n%s", smb, rs)
	}

	// Windows discovery off, macOS discovery on.
	f.cmds = nil
	if err := s.SetSettings(ctx, store.Settings{WSDDisabled: true}); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.cmds, "\n")
	if !strings.Contains(all, "systemctl enable --now smbd nmbd") || !strings.Contains(all, "systemctl disable --now wsdd2") {
		t.Errorf("commands:\n%s", all)
	}
	if _, err := os.Stat(filepath.Join(s.AvahiDir, "nasd-smb.service")); err != nil {
		t.Error("Bonjour announcement must stay when only Windows discovery is off")
	}
	s.SetSettings(ctx, store.Settings{BonjourDisabled: true})
	if _, err := os.Stat(filepath.Join(s.AvahiDir, "nasd-smb.service")); err == nil {
		t.Error("Bonjour announcement must be removed when macOS discovery is off")
	}
	if !strings.Contains(strings.Join(f.cmds, "\n"), "systemctl enable --now wsdd2") {
		t.Error("Windows discovery must come back on")
	}
}
