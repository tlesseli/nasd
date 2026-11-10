package confbackup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readynas/internal/store"
)

type fake struct {
	cmds  []string
	input []string
	smb   []string // content of the files given to pdbedit -i
}

func (f *fake) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, cmd)
	if name == "pdbedit" && len(args) == 2 && args[0] == "-i" {
		data, _ := os.ReadFile(strings.TrimPrefix(args[1], "smbpasswd:"))
		f.smb = append(f.smb, string(data))
	}
	if strings.HasPrefix(cmd, "pdbedit -L -w -u ") {
		return []byte(args[3] + ":1001:XXXX:ABCDEF0123456789ABCDEF0123456789:[U          ]:LCT-6560A1B2:\n"), nil
	}
	if strings.HasPrefix(cmd, "timedatectl show") {
		return []byte("Europe/Brussels\n"), nil
	}
	return nil, nil
}

func (f *fake) runInput(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
	f.input = append(f.input, name+" "+strings.Join(args, " ")+" <- "+stdin)
	return nil, nil
}

func newTest(t *testing.T) (*Service, *fake, string) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "state.json"))
	f := &fake{}
	vol := filepath.Join(dir, "vol")
	os.MkdirAll(vol, 0o755)
	s := NewService(st, "test")
	s.Run, s.RunInput = f.run, f.runInput
	s.Shadow = filepath.Join(dir, "shadow")
	s.SSHUser = "no-such-user-nasd-test"
	s.Group = "no-such-group-nasd-test"
	s.Mounts = func(context.Context) (map[string]string, error) { return map[string]string{"data": vol}, nil }
	return s, f, vol
}

func TestEncryptDecrypt(t *testing.T) {
	b := Bundle{Format: format, Hostname: "nas", State: store.State{Shares: []store.Share{{Name: "photos"}}}}
	data, err := Encrypt(b, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "photos") {
		t.Fatal("the file is not encrypted")
	}
	got, err := Decrypt(data, "correct horse battery")
	if err != nil || got.Hostname != "nas" || got.State.Shares[0].Name != "photos" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := Decrypt(data, "wrong passphrase"); err != ErrPassphrase {
		t.Errorf("wrong passphrase: %v", err)
	}
	data[len(magic)+2] ^= 1 // the header (iterations) is authenticated too
	if _, err := Decrypt(data, "correct horse battery"); err == nil {
		t.Error("an altered file was accepted")
	}
	if _, err := Decrypt([]byte("hello"), "x"); err == nil {
		t.Error("not a settings file")
	}
}

func TestNightlyCopies(t *testing.T) {
	s, _, vol := newTest(t)
	if err := s.CopyNow(context.Background()); err == nil {
		t.Fatal("copied without a passphrase")
	}
	s.Store.Update(func(st *store.State) error { st.ConfigBackup.Passphrase = "0123456789"; return nil })
	dir := filepath.Join(vol, dirName)
	os.MkdirAll(dir, 0o700)
	for d := 1; d <= 20; d++ { // older copies, one made under another hostname
		host := "nas"
		if d == 3 {
			host = "old-name"
		}
		os.WriteFile(filepath.Join(dir, FileName(host, time.Date(2026, 10, d, 3, 30, 0, 0, time.UTC))), []byte("x"), 0o600)
	}
	s.Now = func() time.Time { return time.Date(2026, 11, 10, 3, 30, 0, 0, time.UTC) }
	if err := s.CopyNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.nasd"))
	if len(files) != keep {
		t.Errorf("%d copies kept, want %d", len(files), keep)
	}
	host, _ := os.Hostname()
	if _, err := os.Stat(filepath.Join(dir, FileName(host, s.Now()))); err != nil {
		t.Errorf("today's copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName("nas", time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)))); err == nil {
		t.Error("an old copy was kept")
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", fi.Mode().Perm())
	}
	if st := s.Store.Read().ConfigBackup; st.LastCopy.IsZero() || st.LastError != "" {
		t.Errorf("status %+v", st)
	}
	copies := s.Copies(context.Background())
	if len(copies) != keep || copies[0].Volume != "data" {
		t.Errorf("copies: %+v", copies)
	}
	if _, err := s.ReadCopy(context.Background(), "data", "../../etc/shadow"); err == nil {
		t.Error("path outside the copies accepted")
	}
}

func TestCollectAndRestore(t *testing.T) {
	s, f, _ := newTest(t)
	ctx := context.Background()
	os.WriteFile(s.Shadow, []byte("root:*:1:0:99999:7:::\nalice:$y$j9T$abc:1:0:99999:7:::\n"), 0o600)

	// The backup: alice, a share, a backup job, a network and fan that
	// must not come back.
	saved := store.State{
		Users:        []store.User{{Name: "admin", Admin: true, PasswordHash: "old-admin"}, {Name: "alice"}},
		Shares:       []store.Share{{Name: "photos", Volume: "data"}, {Name: "films", Volume: "media"}},
		Backups:      []store.BackupJob{{ID: 3, Name: "usb"}},
		NextBackupID: 4,
		Network:      store.NetworkSettings{Managed: true, Address: "10.0.0.9/24"},
		Fan:          store.FanSettings{Mode: "full"},
		Maintenance:  map[string]store.Maintenance{"data": {FsckAtBoot: true}},
	}
	b := Bundle{Format: format, Created: time.Now(), Hostname: "", State: saved, GroupGID: 64990,
		Accounts: []Account{{Name: "alice", UID: 64991, LinuxHash: "$y$j9T$abc", SMBEntry: "alice:1001:XXXX:NTHASH:[U          ]:LCT-1:"}}}

	// The running system: a new admin password, a user made since, a network.
	s.Store.Update(func(st *store.State) error {
		st.Users = []store.User{{Name: "admin", Admin: true, PasswordHash: "new-admin"}, {Name: "bob"}}
		st.Network = store.NetworkSettings{DHCP: true}
		st.Fan = store.FanSettings{Mode: "curve", MinPercent: 38}
		st.Events = []store.Event{{Kind: "system_started"}}
		st.NextBackupID = 9
		return nil
	})
	var log []string
	logf := func(format string, a ...any) { log = append(log, format) }
	if err := s.Restore(ctx, f.run, logf, b, "the-passphrase"); err != nil {
		t.Fatal(err)
	}
	st := s.Store.Read()
	if len(st.Users) != 3 || st.Users[0].PasswordHash != "new-admin" || st.Users[1].Name != "alice" || st.Users[2].Name != "bob" {
		t.Errorf("users: %+v", st.Users)
	}
	if len(st.Shares) != 2 || len(st.Backups) != 1 || st.NextBackupID != 9 {
		t.Errorf("shares/backups: %+v %+v %d", st.Shares, st.Backups, st.NextBackupID)
	}
	if !st.Network.DHCP || st.Fan.Mode != "curve" || len(st.Events) != 1 || st.Maintenance["data"].FsckAtBoot {
		t.Errorf("kept from the running system: %+v %+v %+v", st.Network, st.Fan, st.Events)
	}
	if st.ConfigBackup.Passphrase != "the-passphrase" {
		t.Errorf("passphrase %q", st.ConfigBackup.Passphrase)
	}
	cmds := strings.Join(f.cmds, "\n")
	for _, want := range []string{
		"groupadd --gid 64990 " + s.Group,
		"useradd --no-create-home --no-user-group --gid " + s.Group + " --home-dir /nonexistent --shell /usr/sbin/nologin --comment NAS user --uid 64991 alice",
	} {
		if !strings.Contains(cmds, want) {
			t.Errorf("missing %q in:\n%s", want, cmds)
		}
	}
	if strings.Contains(cmds, "$y$") || strings.Contains(cmds, "NTHASH") {
		t.Error("a password hash went into command arguments")
	}
	if len(f.input) != 1 || f.input[0] != "chpasswd -e <- alice:$y$j9T$abc\n" {
		t.Errorf("chpasswd: %v", f.input)
	}
	// The uid field is the new account's (here the backup's: it was free).
	if len(f.smb) != 1 || !strings.HasPrefix(f.smb[0], "alice:64991:XXXX:NTHASH:") {
		t.Errorf("smb entry: %v", f.smb)
	}
	if !strings.Contains(strings.Join(log, "\n"), "isn't mounted here") {
		t.Errorf("no warning for the share on a missing volume: %v", log)
	}
}
