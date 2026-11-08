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

	"readynas/internal/api"
	"readynas/internal/auth"
	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/run"
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

	srv := &api.Server{
		Store:    st,
		Disks:    disks.NewService(run.Exec),
		Volumes:  volumes.NewService(run.Exec),
		Jobs:     jobs.NewManager(run.Exec),
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
