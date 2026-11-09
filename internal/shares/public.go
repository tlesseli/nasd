package shares

// Shares shown in the public folder. A share marked "visible in Public"
// appears as \\nas\public\<share>: a read-only bind mount of the share's
// folder inside the public folder. Read-only is enforced by the kernel, so
// it holds even when guests may write in the public folder itself.
//
//	/data/media/public/Photos  ──bind, ro──▶  /data/media/Photos
//
// Bind mounts don't survive a restart: Apply (at startup) recreates them,
// and every change of shares or of the public folder brings them in line.

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"readynas/internal/store"
)

// publicMountsWanted: mount point -> source folder.
func publicMountsWanted(st store.State) map[string]string {
	want := map[string]string{}
	p := st.Settings.PublicShare
	if !p.Enabled || p.Path == "" {
		return want
	}
	for _, sh := range online(st.Shares) {
		if sh.InPublic && sh.Path != "" && sh.Path != p.Path {
			want[filepath.Join(p.Path, sh.Name)] = sh.Path
		}
	}
	return want
}

// mountpoints lists the current mount points (from /proc/self/mounts,
// where a space is written \040).
func (s *Service) mountpoints() []string {
	data, err := os.ReadFile(s.ProcMounts)
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			out = append(out, strings.ReplaceAll(f[1], `\040`, " "))
		}
	}
	return out
}

// syncPublicMounts mounts what is wanted and unmounts the rest: anything
// mounted directly inside a "public" folder of a volume (the current one or
// one used before). Errors are logged: Samba still works without them.
func (s *Service) syncPublicMounts(ctx context.Context, st store.State) {
	want := publicMountsWanted(st)
	mounted := map[string]bool{}
	for _, m := range s.mountpoints() {
		if filepath.Base(filepath.Dir(m)) == PublicName {
			mounted[m] = true
		}
	}
	for m := range mounted {
		if _, ok := want[m]; ok {
			continue
		}
		if _, err := s.Run(ctx, "umount", m); err != nil {
			log.Printf("public folder: cannot unmount %s: %v", m, err)
			continue
		}
		os.Remove(m) // the empty mount point (only removed if empty)
	}
	for m, src := range want {
		if mounted[m] {
			continue
		}
		// A real folder of the same name in the public folder: leave it be.
		if entries, err := os.ReadDir(m); err == nil && len(entries) > 0 {
			log.Printf("public folder: %s already exists, share %s not shown there", m, filepath.Base(m))
			continue
		}
		if err := os.MkdirAll(m, 0o755); err != nil {
			log.Printf("public folder: %v", err)
			continue
		}
		if _, err := s.Run(ctx, "mount", "--bind", src, m); err != nil {
			log.Printf("public folder: cannot show %s: %v", src, err)
			continue
		}
		// A bind mount starts read-write; read-only needs a second step.
		if _, err := s.Run(ctx, "mount", "-o", "remount,bind,ro", m); err != nil {
			log.Printf("public folder: %s left visible read-write? unmounting: %v", m, err)
			s.Run(ctx, "umount", m)
		}
	}
}
