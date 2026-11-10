// Package confbackup saves and restores nasd's settings: users, shares,
// backup jobs, alerts, schedules... everything in state.json, plus what lives
// outside it: the users' password hashes (Linux and Samba), the administrator
// SSH keys, the hostname and time zone.
//
// The files hold secrets (password hashes, cloud keys), so they are always
// encrypted with a passphrase: AES-256-GCM, with a key derived from the
// passphrase by PBKDF2-SHA256. Without the passphrase a copy is useless.
//
// Copies are made by hand (download) and every night onto a data volume, in
// /data/<volume>/.nasd-settings/ (root only). When the system has to be
// reinstalled, the volume is adopted again and its copies show up in
// System > Settings backup, ready to restore.
package confbackup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"readynas/internal/run"
	"readynas/internal/store"
)

// Bundle is what a settings file contains (once decrypted).
type Bundle struct {
	Format   int         `json:"format"`
	Created  time.Time   `json:"created"`
	Version  string      `json:"version"` // nasd version that made it
	Hostname string      `json:"hostname"`
	Timezone string      `json:"timezone"`
	State    store.State `json:"state"`
	// GroupGID: the group owning shared files. Files on the volumes carry
	// numeric IDs, so a reinstalled system must use the same numbers.
	GroupGID int       `json:"groupGID"`
	Accounts []Account `json:"accounts"`
	SSHKeys  []string  `json:"sshKeys"`
}

// Account is a sharing user's system side.
type Account struct {
	Name      string `json:"name"`
	UID       int    `json:"uid"`
	LinuxHash string `json:"linuxHash"` // /etc/shadow (FTP signs in through PAM)
	SMBEntry  string `json:"smbEntry"`  // "pdbedit -L -w" line: the SMB password hash
}

const (
	format     = 1
	magic      = "NASD-SETTINGS\n"
	iterations = 600_000 // PBKDF2: about a second on the ReadyNAS's Atom
	// MinPassphrase: the passphrase is all that protects the copies.
	MinPassphrase = 10
	keep          = 14 // nightly copies kept per volume
	dirName       = ".nasd-settings"
	suffix        = ".nasd"
)

type Service struct {
	Store    *store.Store
	Run      run.Func
	RunInput run.InputFunc
	Version  string
	Group    string // "nasusers"
	Shadow   string // "/etc/shadow"
	SSHUser  string // "nas": whose authorized_keys are saved
	// Mounts: data volumes that are mounted, name -> mount point.
	Mounts func(ctx context.Context) (map[string]string, error)
	Notify func(kind, subject, body string)
	Now    func() time.Time

	// Restore hooks, set by main: they belong to other packages.
	Reapply     func(ctx context.Context) error // smb.conf, quotas, power, updates...
	SetHostname func(ctx context.Context, name string) error
	SetTimezone func(ctx context.Context, tz string) error

	mu sync.Mutex // one copy at a time
}

func NewService(st *store.Store, version string) *Service {
	return &Service{Store: st, Run: run.Exec, RunInput: run.ExecInput, Version: version, Group: "nasusers",
		Shadow: "/etc/shadow", SSHUser: "nas", Now: time.Now, Notify: func(string, string, string) {}}
}

// ------------------------------------------------------------ collect

// Collect gathers the current settings.
func (s *Service) Collect(ctx context.Context) (Bundle, error) {
	st := s.Store.Read()
	st.Events, st.NetworkPending, st.FirewallPending = nil, nil, nil // a log, and changes in progress: not settings
	b := Bundle{Format: format, Created: s.Now(), Version: s.Version, State: st, Accounts: []Account{}, SSHKeys: []string{}}
	b.Hostname, _ = os.Hostname()
	if out, err := s.Run(ctx, "timedatectl", "show", "-p", "Timezone", "--value"); err == nil {
		b.Timezone = strings.TrimSpace(string(out))
	}
	if g, err := user.LookupGroup(s.Group); err == nil {
		b.GroupGID, _ = strconv.Atoi(g.Gid)
	}
	shadow := readShadow(s.Shadow)
	for _, u := range st.Users {
		if u.Admin {
			continue // the web admin lives in the state only
		}
		// A user whose system side is broken is still saved (the restore
		// then asks to set their password again): one bad account must
		// not stop every copy.
		a := Account{Name: u.Name, LinuxHash: shadow[u.Name]}
		if lu, err := user.Lookup(u.Name); err == nil {
			a.UID, _ = strconv.Atoi(lu.Uid)
		} else {
			log.Printf("settings copy: user %s has no system account", u.Name)
		}
		if out, err := s.Run(ctx, "pdbedit", "-L", "-w", "-u", u.Name); err == nil {
			a.SMBEntry = strings.TrimSpace(string(out))
		} else {
			log.Printf("settings copy: no SMB account for %s: %v", u.Name, err)
		}
		b.Accounts = append(b.Accounts, a)
	}
	if path, _, err := s.authorizedKeys(); err == nil {
		data, _ := os.ReadFile(path)
		for _, l := range strings.Split(string(data), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				b.SSHKeys = append(b.SSHKeys, l)
			}
		}
	}
	return b, nil
}

// readShadow: user -> password hash.
func readShadow(path string) map[string]string {
	m := map[string]string{}
	data, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(data), "\n") {
		if f := strings.Split(l, ":"); len(f) > 2 {
			m[f[0]] = f[1]
		}
	}
	return m
}

func (s *Service) authorizedKeys() (string, *user.User, error) {
	u, err := user.Lookup(s.SSHUser)
	if err != nil {
		return "", nil, err
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), u, nil
}

// ------------------------------------------------------------ encryption

// Encrypt makes a settings file:
//
//	"NASD-SETTINGS\n" | iterations (4 bytes) | salt (16) | nonce (12) | AES-GCM(gzip(JSON))
//
// The header is authenticated too: a file can't be altered unnoticed.
func Encrypt(b Bundle, passphrase string) ([]byte, error) {
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	if err := json.NewEncoder(zw).Encode(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	salt, nonce := make([]byte, 16), make([]byte, 12)
	rand.Read(salt)
	rand.Read(nonce)
	header := binary.BigEndian.AppendUint32([]byte(magic), iterations)
	header = append(append(header, salt...), nonce...)
	gcm, err := newGCM(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	return gcm.Seal(header, nonce, plain.Bytes(), header), nil
}

// ErrPassphrase: the passphrase doesn't open this file (or it was damaged).
var ErrPassphrase = errors.New("wrong passphrase, or the file is damaged")

func Decrypt(data []byte, passphrase string) (Bundle, error) {
	const headerLen = len(magic) + 4 + 16 + 12
	if len(data) < headerLen || string(data[:len(magic)]) != magic {
		return Bundle{}, errors.New("this is not a nasd settings file")
	}
	iter := int(binary.BigEndian.Uint32(data[len(magic):]))
	if iter < 1 || iter > 10_000_000 {
		return Bundle{}, errors.New("this is not a nasd settings file")
	}
	salt := data[len(magic)+4 : len(magic)+20]
	nonce := data[len(magic)+20 : headerLen]
	gcm, err := newGCM(passphrase, salt, iter)
	if err != nil {
		return Bundle{}, err
	}
	plain, err := gcm.Open(nil, nonce, data[headerLen:], data[:headerLen])
	if err != nil {
		return Bundle{}, ErrPassphrase
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return Bundle{}, err
	}
	var b Bundle
	if err := json.NewDecoder(io.LimitReader(zr, 64<<20)).Decode(&b); err != nil {
		return Bundle{}, fmt.Errorf("unreadable settings: %w", err)
	}
	if b.Format != format {
		return Bundle{}, fmt.Errorf("settings format %d: this nasd reads format %d (update nasd first)", b.Format, format)
	}
	return b, nil
}

func newGCM(passphrase string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// ------------------------------------------------------------ copies

// FileName: nasd-settings-<host>-2026-11-10.nasd
func FileName(host string, t time.Time) string {
	return "nasd-settings-" + host + "-" + t.Format("2006-01-02") + suffix
}

// Make collects and encrypts the settings with the stored passphrase.
func (s *Service) Make(ctx context.Context) ([]byte, string, error) {
	pass := s.Store.Read().ConfigBackup.Passphrase
	if pass == "" {
		return nil, "", errors.New("set a passphrase first")
	}
	b, err := s.Collect(ctx)
	if err != nil {
		return nil, "", err
	}
	data, err := Encrypt(b, pass)
	return data, FileName(b.Hostname, b.Created), err
}

// targetVolume: the chosen volume, or the first mounted one.
func (s *Service) targetVolume(ctx context.Context) (string, string, error) {
	mounts, err := s.Mounts(ctx)
	if err != nil {
		return "", "", err
	}
	want := s.Store.Read().ConfigBackup.Volume
	if want != "" {
		if m, ok := mounts[want]; ok {
			return want, m, nil
		}
		return "", "", fmt.Errorf("volume %s isn't mounted", want)
	}
	names := make([]string, 0, len(mounts))
	for n := range mounts {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", "", errors.New("no data volume to keep the copy on")
	}
	return names[0], mounts[names[0]], nil
}

// CopyNow writes today's encrypted copy onto the data volume and keeps the
// last 14. The result is recorded in the state (shown in the interface).
func (s *Service) CopyNow(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.copy(ctx)
	s.Store.Update(func(st *store.State) error {
		if err == nil {
			st.ConfigBackup.LastCopy, st.ConfigBackup.LastError = s.Now(), ""
		} else {
			st.ConfigBackup.LastError = err.Error()
		}
		return nil
	})
	return err
}

func (s *Service) copy(ctx context.Context) error {
	data, name, err := s.Make(ctx)
	if err != nil {
		return err
	}
	_, mnt, err := s.targetVolume(ctx)
	if err != nil {
		return err
	}
	// Root only: on a volume whose root is shared, users see the folder
	// but can't open it.
	dir := filepath.Join(mnt, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	os.Chmod(dir, 0o700)
	tmp := filepath.Join(dir, ".tmp-"+name)
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	// Keep the newest copies: by the date at the end of the name (the
	// hostname before it may have changed).
	files, _ := filepath.Glob(filepath.Join(dir, "nasd-settings-*"+suffix))
	date := func(f string) string {
		b := strings.TrimSuffix(filepath.Base(f), suffix)
		return b[max(0, len(b)-10):]
	}
	sort.Slice(files, func(i, j int) bool { return date(files[i]) > date(files[j]) })
	for i, f := range files {
		if i >= keep {
			os.Remove(f)
		}
	}
	return nil
}

// Copy is a settings file found on a data volume.
type Copy struct {
	Volume string    `json:"volume"`
	Name   string    `json:"name"`
	Time   time.Time `json:"time"`
	Size   int64     `json:"size"`
}

// Copies lists the settings files on every mounted data volume, newest first.
func (s *Service) Copies(ctx context.Context) []Copy {
	out := []Copy{}
	mounts, _ := s.Mounts(ctx)
	for vol, mnt := range mounts {
		files, _ := filepath.Glob(filepath.Join(mnt, dirName, "nasd-settings-*"+suffix))
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil {
				out = append(out, Copy{Volume: vol, Name: filepath.Base(f), Time: fi.ModTime(), Size: fi.Size()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

// ReadCopy returns the content of a copy listed by Copies.
func (s *Service) ReadCopy(ctx context.Context, volume, name string) ([]byte, error) {
	if name != filepath.Base(name) || !strings.HasSuffix(name, suffix) || !strings.HasPrefix(name, "nasd-settings-") {
		return nil, errors.New("invalid copy name")
	}
	mounts, err := s.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	mnt, ok := mounts[volume]
	if !ok {
		return nil, fmt.Errorf("volume %s isn't mounted", volume)
	}
	return os.ReadFile(filepath.Join(mnt, dirName, name))
}

// RunNightly makes the nightly copy at 03:30 when it is switched on.
func (s *Service) RunNightly(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	last := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := s.Now()
		cfg := s.Store.Read().ConfigBackup
		day := now.Format("2006-01-02")
		if !cfg.Nightly || cfg.Passphrase == "" || now.Hour() != 3 || now.Minute() < 30 || last == day {
			continue
		}
		last = day
		if err := s.CopyNow(ctx); err != nil {
			log.Printf("settings copy: %v", err)
			s.Notify("settings_backup_failed", "The nightly copy of the settings failed", err.Error())
		}
	}
}
