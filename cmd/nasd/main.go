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
	"strings"
	"syscall"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/api"
	"readynas/internal/auth"
	"readynas/internal/backups"
	"readynas/internal/disks"
	"readynas/internal/hardware"
	"readynas/internal/jobs"
	"readynas/internal/lcd"
	"readynas/internal/metrics"
	"readynas/internal/monitor"
	"readynas/internal/power"
	"readynas/internal/run"
	"readynas/internal/shares"
	"readynas/internal/store"
	"readynas/internal/sysconf"
	"readynas/internal/tlsutil"
	"readynas/internal/volumes"
	"readynas/web"
)

// version is set at build time: -ldflags "-X main.version=$(git describe)".
var version = "dev"

func main() {
	listen := flag.String("listen", ":8443", "HTTPS listen address")
	dataDir := flag.String("data", "/var/lib/nasd", "directory for state and TLS certificate")
	hwmon := flag.String("hwmon", "/sys", "root of the sensors tree (a fake one for tests)")
	fansFull := flag.Bool("fans-full", false, "set every manually controlled fan to full speed and exit (run by systemd after nasd stops)")
	lcdMessage := flag.String("lcd-message", "", `show "line 1|line 2" on the front display and exit (run by systemd around nasd)`)
	flag.Parse()
	if *lcdMessage != "" {
		l1, l2, _ := strings.Cut(*lcdMessage, "|")
		lcd.ShowOnce(lcd.Detect(), l1, l2)
		return
	}
	if *fansFull {
		hardware.AllFansFull(*hwmon)
		return
	}
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
	shr.CertDir = *dataDir // FTPS reuses the web interface's certificate
	shr.QuotaMounts = func(ctx context.Context) ([]string, error) {
		list, err := vols.List(ctx)
		var out []string
		for _, v := range list {
			if v.Quotas {
				out = append(out, v.Mountpoint)
			}
		}
		return out, err
	}
	// Make smb.conf match state.json (e.g. after an update or a manual edit).
	if err := shr.Apply(context.Background()); err != nil {
		log.Printf("warning: applying Samba config: %v", err)
	}

	jm := jobs.NewManager(run.Exec)
	jm.EnvExec = run.ExecEnv
	al := alerts.NewService(st)
	bk := backups.NewService(st, jm, *dataDir)
	bk.Notify = al.Notify
	dk := disks.NewService(run.Exec)
	mon := monitor.New(st, vols, dk, al)
	pw := power.NewService(st, dk, jm)
	pw.Notify = al.Notify
	mon.Power = pw
	mon.Sys = *hwmon
	fan := hardware.NewFan(st, dk)
	fan.Root = *hwmon
	btn := hardware.NewButton(st, bk, jm)
	btn.Notify = al.Notify
	sc := sysconf.NewService(st, jm)
	sc.Notify = al.Notify
	sc.ResumePending() // a network change not confirmed before a restart still rolls back
	afterBootCheck(st, vols, al)

	loc := hardware.NewLocator()
	display := lcd.New(lcdCollector(st, vols, dk, mon, pw, btn, loc, jm, *hwmon), lcd.Detect())
	display.AlwaysOn = func() bool { return st.Read().LCD.AlwaysOn }
	btn.OnPress = display.Wake
	log.Printf("front display: %s", display.Out.Name())

	ms := metrics.New(filepath.Join(*dataDir, "metrics.json"))
	ms.Sys = *hwmon
	ms.Storage = func(ctx context.Context) map[string]int64 {
		list, _ := vols.List(ctx)
		used := map[string]int64{}
		for _, v := range list {
			if v.TotalBytes > 0 {
				used[v.Name] = v.UsedBytes
			}
		}
		return used
	}

	srv := &api.Server{
		Store:    st,
		Disks:    dk,
		Alerts:   al,
		Monitor:  mon,
		Power:    pw,
		System:   sc,
		Run:      run.Exec,
		Sys:      *hwmon,
		Button:   btn,
		Fan:      fan,
		Locator:  loc,
		LCD:      display,
		Metrics:  ms,
		Version:  version,
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
	go pw.RunTimer(ctx)
	go bk.RunUSBWatcher(ctx)
	go btn.Run(ctx)
	go fan.Run(ctx)
	go ms.Run(ctx)
	go display.Run(ctx)
	display.Wake() // routine screen lit for 2 min after start
	go shr.RunSizes(ctx)
	go func() {
		// state.json is the source of truth for quotas: make the system agree
		// (e.g. after a restore, or a quota left behind by an older version).
		if err := shr.ApplyQuotas(ctx); err != nil {
			log.Printf("warning: applying quotas: %v", err)
		}
	}()
	go func() {
		// Spin-down and Wake-on-LAN don't survive a reboot: apply them again.
		if err := pw.Apply(ctx); err != nil {
			log.Printf("warning: applying power settings: %v", err)
		}
	}()
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
	display.SetPhase("Shutting down", "nasd stopped")
	ms.Save() // synchronously: the process exits right after, the sampler goroutine may not get to it
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
