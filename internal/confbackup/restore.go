package confbackup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"slices"
	"strconv"
	"strings"

	"readynas/internal/run"
	"readynas/internal/store"
)

// Restore puts a bundle's settings back. It runs as a job (r logs each
// command). What it does, in order:
//
//  1. user accounts: created with their old numeric IDs (files on the
//     volumes belong to those numbers) and their old password hashes, so
//     everyone keeps their password;
//  2. the state: shares, backup jobs, alerts, schedules... Kept from the
//     running system: the web admin account (no surprise password change),
//     the network (it could cut the NAS off), the fan and button
//     calibration (measured on this hardware) and the event log. Users
//     created since the backup are kept too;
//  3. SSH keys from the backup are added (none is removed);
//  4. everything is applied (Samba, FTP, quotas, power, updates);
//  5. hostname and time zone, last: nasd restarts to use them.
//
// passphrase becomes the stored one: it opened these copies.
func (s *Service) Restore(ctx context.Context, r run.Func, logf func(string, ...any), b Bundle, passphrase string) error {
	gid := s.restoreGroup(ctx, r, logf, b.GroupGID)
	var errs []error
	for _, a := range b.Accounts {
		if err := s.restoreAccount(ctx, r, logf, a, gid); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", a.Name, err))
			logf("user %s: %v", a.Name, err)
		}
	}

	var kept []string
	err := s.Store.Update(func(cur *store.State) error {
		next := b.State
		next.Events, next.NetworkPending = cur.Events, cur.NetworkPending
		next.Network, next.Fan, next.Button = cur.Network, cur.Fan, cur.Button
		next.DiskTests.Reported = cur.DiskTests.Reported
		next.Updates.LastReported = cur.Updates.LastReported
		next.ConfigBackup.Passphrase = passphrase
		next.ConfigBackup.LastCopy, next.ConfigBackup.LastError = cur.ConfigBackup.LastCopy, cur.ConfigBackup.LastError
		next.NextBackupID = max(next.NextBackupID, cur.NextBackupID)
		for name, m := range next.Maintenance {
			m.FsckAtBoot = false
			next.Maintenance[name] = m
		}
		// Users: this system's admins, the backup's users, then users
		// created here since the backup.
		var users []store.User
		for _, u := range cur.Users {
			if u.Admin {
				users = append(users, u)
			}
		}
		for _, u := range next.Users {
			if !u.Admin {
				users = append(users, u)
			}
		}
		for _, u := range cur.Users {
			if !u.Admin && !slices.ContainsFunc(next.Users, func(x store.User) bool { return x.Name == u.Name }) {
				users = append(users, u)
				kept = append(kept, u.Name)
			}
		}
		next.Users = users
		*cur = next
		return nil
	})
	if err != nil {
		return err
	}
	logf("settings restored: %d users, %d shares, %d backup jobs", len(b.Accounts), len(b.State.Shares), len(b.State.Backups))
	if len(kept) > 0 {
		logf("kept users created since the backup: %s", strings.Join(kept, ", "))
	}

	if n, err := s.addSSHKeys(b.SSHKeys); err != nil {
		logf("SSH keys: %v", err)
	} else if n > 0 {
		logf("added %d SSH keys from the backup", n)
	}

	if mounts, err := s.Mounts(ctx); err == nil {
		for _, sh := range b.State.Shares {
			if _, ok := mounts[sh.Volume]; !ok {
				logf("warning: share %s is on volume %s, which isn't mounted here (adopt it under that name)", sh.Name, sh.Volume)
			}
		}
	}
	if s.Reapply != nil {
		if err := s.Reapply(ctx); err != nil {
			errs = append(errs, err)
			logf("applying the settings: %v", err)
		}
	}

	host, _ := os.Hostname()
	restart := false
	if b.Timezone != "" && s.SetTimezone != nil {
		if out, _ := s.Run(ctx, "timedatectl", "show", "-p", "Timezone", "--value"); strings.TrimSpace(string(out)) != b.Timezone {
			if err := s.SetTimezone(ctx, b.Timezone); err != nil {
				logf("time zone %s: %v", b.Timezone, err)
			} else {
				logf("time zone: %s", b.Timezone)
				restart = true
			}
		}
	}
	if b.Hostname != "" && b.Hostname != host && s.SetHostname != nil {
		if err := s.SetHostname(ctx, b.Hostname); err != nil {
			logf("hostname %s: %v", b.Hostname, err)
		} else {
			logf("hostname: %s", b.Hostname)
			restart = true
		}
	}
	if restart {
		logf("nasd restarts to use them: reload the page in a few seconds")
	}
	s.Notify("settings_restored", "Settings restored", fmt.Sprintf("The settings saved on %s (%s) were restored.", b.Created.Format("2006-01-02 15:04"), b.Hostname))
	return errors.Join(errs...)
}

// restoreGroup makes sure the shared files' group exists, with its old
// number when possible. Returns the group's number.
func (s *Service) restoreGroup(ctx context.Context, r run.Func, logf func(string, ...any), want int) int {
	if g, err := user.LookupGroup(s.Group); err == nil {
		gid, _ := strconv.Atoi(g.Gid)
		if want != 0 && gid != want {
			logf("warning: group %s is %d here but was %d: files from before belong to group %d", s.Group, gid, want, want)
		}
		return gid
	}
	args := []string{s.Group}
	if _, err := user.LookupGroupId(strconv.Itoa(want)); want != 0 && err != nil {
		args = []string{"--gid", strconv.Itoa(want), s.Group}
	}
	r(ctx, "groupadd", args...)
	if g, err := user.LookupGroup(s.Group); err == nil {
		gid, _ := strconv.Atoi(g.Gid)
		return gid
	}
	return want
}

func (s *Service) restoreAccount(ctx context.Context, r run.Func, logf func(string, ...any), a Account, gid int) error {
	uid := a.UID
	if u, err := user.Lookup(a.Name); err == nil {
		// Only a NAS user may be overwritten, never a system account
		// that happens to have the same name.
		if g, _ := strconv.Atoi(u.Gid); g != gid {
			return errors.New("a system account has this name: skipped")
		}
		uid, _ = strconv.Atoi(u.Uid)
		if uid != a.UID {
			logf("warning: %s is user %d here but was %d: their files from before need chown", a.Name, uid, a.UID)
		}
	} else {
		args := []string{"--no-create-home", "--no-user-group", "--gid", s.Group,
			"--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--comment", "NAS user"}
		if _, taken := user.LookupId(strconv.Itoa(a.UID)); a.UID > 0 && taken != nil {
			args = append(args, "--uid", strconv.Itoa(a.UID))
		} else {
			logf("warning: user number %d is taken: %s gets a new one, their files from before need chown", a.UID, a.Name)
		}
		if _, err := r(ctx, "useradd", append(args, a.Name)...); err != nil {
			return err
		}
		if u, err := user.Lookup(a.Name); err == nil {
			uid, _ = strconv.Atoi(u.Uid)
		}
	}
	// Hashes go through stdin and a private file: never in arguments,
	// which every user can read with ps.
	if a.LinuxHash != "" {
		if _, err := s.RunInput(ctx, a.Name+":"+a.LinuxHash+"\n", "chpasswd", "-e"); err != nil {
			return err
		}
	}
	// "name:uid:LM:NT:flags:LCT:": the uid field must be this system's.
	f := strings.SplitN(a.SMBEntry, ":", 3)
	if len(f) < 3 || f[0] != a.Name {
		return errors.New("no SMB password in the backup: set it again in Users")
	}
	tmp, err := os.CreateTemp("", "nasd-smb-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	tmp.WriteString(a.Name + ":" + strconv.Itoa(uid) + ":" + f[2] + "\n")
	tmp.Close()
	r(ctx, "pdbedit", "-x", "-u", a.Name) // replaced if it exists
	if _, err := r(ctx, "pdbedit", "-i", "smbpasswd:"+tmp.Name()); err != nil {
		return err
	}
	logf("user %s restored (with their password)", a.Name)
	return nil
}

// addSSHKeys adds the backup's keys the system doesn't have yet.
func (s *Service) addSSHKeys(keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	path, u, err := s.authorizedKeys()
	if err != nil {
		return 0, err
	}
	data, _ := os.ReadFile(path)
	have := strings.Split(string(data), "\n")
	var add []string
	for _, k := range keys {
		if strings.HasPrefix(k, "ssh-") || strings.HasPrefix(k, "ecdsa-") || strings.HasPrefix(k, "sk-") {
			if !slices.Contains(have, k) && !slices.Contains(add, k) {
				add = append(add, k)
			}
		}
	}
	if len(add) == 0 {
		return 0, nil
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	dir := strings.TrimSuffix(path, "/authorized_keys")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	os.Chown(dir, uid, gid)
	content := strings.TrimRight(string(data), "\n")
	if content != "" {
		content += "\n"
	}
	content += strings.Join(add, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return 0, err
	}
	return len(add), os.Chown(path, uid, gid)
}
