// nasd is the NAS management daemon: it serves the web UI and drives the
// system tools (lsblk, smartctl, later mdadm and samba). It runs as root
// because those tools need it; that's why every endpoint requires login.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/api"
	"readynas/internal/auth"
	"readynas/internal/backups"
	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/monitor"
	"readynas/internal/run"
	"readynas/internal/shares"
	"readynas/internal/store"
	"readynas/internal/tlsutil"
	"readynas/internal/volumes"
	"readynas/web"
)

func main() {
	listen := flag.String("listen", ":8443", "HTTPS listen address")
	dataDir := flag.String("data", "/var/lib/nasd", "directory for state and TLS certificate")
	flag.Parse()
	log.SetFlags(0) // journald already adds timestamps

	if os.Geteuid() != 0 {
		log.Print("warning: not running as root, disk health and RAID management will fail")
	}
	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(filepath.Join(*dataDir, "state.json"))
	if err != nil {
		log.Fatal(err)
	}
	cert, err := tlsutil.LoadOrCreate(*dataDir)
	if err != nil {
		log.Fatalf("tls: %v", err)
	}

	vols := volumes.NewService(run.Exec)
	// Shares need to know where volumes are mounted.
	mounted := func(ctx context.Context) (map[string]string, error) {
		list, err := vols.List(ctx)
		if err != nil {
			return nil, err
		}
		m := map[string]string{}
		for _, v := range list {
			if v.Mountpoint != "" {
				m[v.Name] = v.Mountpoint
			}
		}
		return m, nil
	}
	shr := shares.NewService(st, mounted)
	// Make smb.conf match state.json (e.g. after an update or a manual edit).
	if err := shr.Apply(context.Background()); err != nil {
		log.Printf("warning: applying Samba config: %v", err)
	}

	jm := jobs.NewManager(run.Exec)
	al := alerts.NewService(st)
	bk := backups.NewService(st, jm, *dataDir)
	bk.Notify = al.Notify
	dk := disks.NewService(run.Exec)
	mon := monitor.New(st, vols, dk, al)
	afterBootCheck(st, vols, al)

	srv := &api.Server{
		Store:    st,
		Disks:    dk,
		Alerts:   al,
		Monitor:  mon,
		Run:      run.Exec,
		Sys:      "/sys",
		Volumes:  vols,
		Shares:   shr,
		Jobs:     jm,
		Backups:  bk,
		Sessions: auth.NewSessions(30 * time.Minute),
		Throttle: auth.NewThrottle(5, 10*time.Minute),
		UI:       web.Dist(),
	}
	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.Handler(),
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Stop cleanly on Ctrl-C or `systemctl stop` (SIGTERM).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go bk.RunScheduler(ctx)
	go mon.Run(ctx)
	al.Notify("system_started", "The NAS started", "nasd started.")
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx)
	}()

	log.Printf("nasd listening on https://%s", *listen)
	if err := httpSrv.ListenAndServeTLS("", ""); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// afterBootCheck: an ext4 check was requested for this boot (it ran while
// systemd mounted the volume). Restore the normal mount-count settings.
func afterBootCheck(st *store.Store, vols *volumes.Service, al *alerts.Service) {
	ctx := context.Background()
	for name, m := range st.Read().Maintenance {
		if !m.FsckAtBoot {
			continue
		}
		v, err := vols.Get(ctx, name)
		if err != nil {
			continue
		}
		if err := vols.ResetBootCheck(ctx, run.Exec, v); err != nil {
			log.Printf("reset boot check of %s: %v", name, err)
			continue
		}
		st.Update(func(s *store.State) error {
			m := s.Maintenance[name]
			m.FsckAtBoot = false
			s.Maintenance[name] = m
			return nil
		})
		al.Notify("scrub_done", "Filesystem of "+name+" checked at boot",
			"The check ran while the NAS started. Its result is in the system logs (source: all, search systemd-fsck).")
	}
}
