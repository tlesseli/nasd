package backups

import (
	"context"
	"strings"
	"sync"
	"testing"

	"readynas/internal/store"
)

type envRecorder struct {
	mu   sync.Mutex
	cmds []string
	envs [][]string
}

func (e *envRecorder) run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cmds = append(e.cmds, name+" "+strings.Join(args, " "))
	e.envs = append(e.envs, env)
	return []byte("2026/11/08 21:30:00 NOTICE:    12.500 MiB / 12.500 MiB, 100%, 1.2 MiB/s, ETA 0s\n"), nil
}

func archiveTarget() store.BackupTarget {
	return store.BackupTarget{Kind: "s3", Provider: "scaleway", Region: "fr-par", Bucket: "my-nas-archive", Path: "/photos/",
		AccessKey: "SCWXXXXXXXXXXXXXXXXX", SecretKey: "s3cr3t-key", EncryptPassword: "a long passphrase", BandwidthKB: 2048}
}

func TestValidateS3(t *testing.T) {
	tg := archiveTarget()
	if err := validateS3(&tg); err != nil || tg.StorageClass != "GLACIER" || tg.Path != "photos" {
		t.Fatalf("defaults: %v %+v", err, tg)
	}
	aws := store.BackupTarget{Kind: "s3", Provider: "aws", Region: "eu-west-3", Bucket: "nas", AccessKey: "AKIAXXXXXXXX", SecretKey: "x"}
	if err := validateS3(&aws); err != nil || aws.StorageClass != "DEEP_ARCHIVE" {
		t.Fatalf("aws default class: %v %q", err, aws.StorageClass)
	}
	for label, mutate := range map[string]func(*store.BackupTarget){
		"provider":     func(t *store.BackupTarget) { t.Provider = "dropbox" },
		"bucket":       func(t *store.BackupTarget) { t.Bucket = "My_Bucket" },
		"class":        func(t *store.BackupTarget) { t.StorageClass = "DEEP_ARCHIVE" }, // not a Scaleway class
		"short pass":   func(t *store.BackupTarget) { t.EncryptPassword = "short" },
		"no secret":    func(t *store.BackupTarget) { t.SecretKey = "" },
		"bad endpoint": func(t *store.BackupTarget) { t.Provider, t.Endpoint = "other", "ftp://x" },
	} {
		tg := archiveTarget()
		mutate(&tg)
		if err := validateS3(&tg); err == nil {
			t.Errorf("%s: expected an error", label)
		}
	}
}

func TestArchiveJob(t *testing.T) {
	s, f, share := newTestService(t)
	rec := &envRecorder{}
	s.Jobs.EnvExec = rec.run
	s.RunInput = func(ctx context.Context, stdin, name string, args ...string) ([]byte, error) {
		if stdin != "a long passphrase\n" || strings.Join(args, " ") != "obscure -" {
			t.Errorf("obscure: %q %v", stdin, args)
		}
		return []byte("OBSCURED\n"), nil
	}
	got := runJob(t, s, store.BackupJob{Name: "Archive", Direction: "push", Share: "Photos", Target: archiveTarget()})
	if got.LastRun.Status != "done" || got.LastRun.Summary != "12.500 MiB transferred" {
		t.Fatalf("last run: %+v", got.LastRun)
	}
	cmd := rec.cmds[0]
	if !strings.HasPrefix(cmd, "rclone --config  copy "+share+"/ secure: ") || !strings.Contains(cmd, "--bwlimit 2048K") || !strings.Contains(cmd, "--exclude .recycle/**") {
		t.Errorf("command: %s", cmd)
	}
	env := strings.Join(rec.envs[0], "\n")
	for _, want := range []string{"RCLONE_CONFIG_ARCHIVE_PROVIDER=Scaleway", "RCLONE_CONFIG_ARCHIVE_ENDPOINT=https://s3.fr-par.scw.cloud",
		"RCLONE_CONFIG_ARCHIVE_STORAGE_CLASS=GLACIER", "RCLONE_CONFIG_SECURE_REMOTE=archive:my-nas-archive/photos", "RCLONE_CONFIG_SECURE_PASSWORD=OBSCURED"} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %q", want)
		}
	}
	for _, c := range append(rec.cmds, f.all()) {
		if strings.Contains(c, "s3cr3t") || strings.Contains(c, "passphrase") {
			t.Errorf("a secret leaked into a command line: %s", c)
		}
	}

	// Secrets are never returned, and kept when saving without them.
	list := s.List()
	if list[0].Target.SecretKey != "" || list[0].Target.EncryptPassword != "" || !list[0].Target.Encrypted {
		t.Errorf("list: %+v", list[0].Target)
	}
	b := list[0]
	b.Mirror = true
	if _, err := s.Save(b); err != nil {
		t.Fatal(err)
	}
	st := s.Store.Read()
	if st.FindBackup(b.ID).Target.SecretKey != "s3cr3t-key" || st.FindBackup(b.ID).Target.EncryptPassword != "a long passphrase" {
		t.Error("secrets must be kept on update")
	}
	rec.cmds = nil
	s.Start(b.ID, "manual")
	s.Jobs.Wait()
	if !strings.HasPrefix(rec.cmds[0], "rclone --config  sync ") {
		t.Errorf("mirror must use sync: %s", rec.cmds[0])
	}

	// Retrieval request goes to the objects themselves (below encryption).
	rec.cmds = nil
	if _, err := s.Thaw(b.ID, 7); err != nil {
		t.Fatal(err)
	}
	s.Jobs.Wait()
	if rec.cmds[0] != "rclone --config  backend restore archive:my-nas-archive/photos -o priority=Bulk -o lifetime=7" {
		t.Errorf("thaw: %s", rec.cmds[0])
	}
}
