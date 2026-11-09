// Package api is the HTTP layer: JSON endpoints under /api/ plus the web UI
// (static files) for everything else.
//
// Endpoints (milestone 2):
//
//	GET  /api/setup    -> {"needsSetup": bool}      public
//	POST /api/setup    {"password"} create admin     public, only once
//	POST /api/login    {"username","password"}       public
//	POST /api/logout
//	GET  /api/me       -> {"username"}
//	GET  /api/system   -> system.Info
//	GET  /api/disks    -> []disks.Disk
//
// Volumes (milestone 3, see volumes.go). Disk operations run as background
// jobs: they answer 202 with a job, which the UI polls on /api/jobs/{id}.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"readynas/internal/alerts"
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
	"readynas/internal/system"
	"readynas/internal/volumes"
)

const (
	cookieName        = "nasd_session"
	adminUser         = "admin"
	minPasswordLength = 8
)

type Server struct {
	Store    *store.Store
	Disks    *disks.Service
	Volumes  *volumes.Service
	Jobs     *jobs.Manager
	Shares   *shares.Service
	Backups  *backups.Service
	Alerts   *alerts.Service
	Monitor  *monitor.Monitor
	Power    *power.Service
	System   *sysconf.Service
	Button   *hardware.Button
	Fan      *hardware.Fan
	Locator  *hardware.Locator
	LCD      *lcd.Engine
	Metrics  *metrics.Sampler
	Version  string
	failed   failedLogins
	Run      run.Func // for read-only commands (smartctl -x, journalctl)
	Sys      string   // "/sys"
	Sessions *auth.Sessions
	Throttle *auth.Throttle
	UI       fs.FS // built Svelte app (web/dist)
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/setup", s.getSetup)
	mux.HandleFunc("POST /api/setup", s.postSetup)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", s.logout)

	mux.Handle("GET /api/me", s.requireLogin(s.me))
	mux.Handle("GET /api/system", s.requireLogin(s.system))
	mux.Handle("GET /api/disks", s.requireLogin(s.disks))
	s.volumeRoutes(mux)
	s.shareRoutes(mux)
	s.backupRoutes(mux)
	s.healthRoutes(mux)
	s.systemRoutes(mux)
	s.users2Routes(mux)
	s.hardwareRoutes(mux)
	s.overviewRoutes(mux)
	mux.Handle("GET /api/lcd", s.requireLogin(s.getLCD))
	mux.Handle("POST /api/lcd", s.requireLogin(s.setLCD))
	mux.Handle("POST /api/lcd/wake", s.requireLogin(s.wakeLCD))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "unknown endpoint")
	})
	mux.Handle("/", spaHandler(s.UI))

	return securityHeaders(csrfGuard(logRequests(mux)))
}

// ---- setup & login ----

func (s *Server) needsSetup() bool {
	return len(s.Store.Read().Users) == 0
}

func (s *Server) getSetup(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"needsSetup": s.needsSetup()})
}

// postSetup creates the admin account on first start. Whoever reaches the
// UI first sets the password, so do it right after installing the NAS.
func (s *Server) postSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if len(req.Password) < minPasswordLength {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	errAlreadyDone := errors.New("setup already done")
	err = s.Store.Update(func(st *store.State) error {
		if len(st.Users) > 0 { // checked inside the lock: no race between two browsers
			return errAlreadyDone
		}
		st.Users = append(st.Users, store.User{Name: adminUser, PasswordHash: hash, Admin: true})
		return nil
	})
	if errors.Is(err, errAlreadyDone) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.startSession(w, adminUser)
	writeJSON(w, http.StatusOK, map[string]string{"username": adminUser})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.Throttle.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again in a few minutes")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	st := s.Store.Read()
	u := st.FindUser(req.Username)
	// Same error whether the user exists or not: don't reveal valid names.
	if u == nil || !u.Admin || !auth.CheckPassword(u.PasswordHash, req.Password) {
		s.Throttle.Fail(ip)
		s.failed.add()
		log.Printf("failed login for %q from %s", req.Username, ip)
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	s.Throttle.Reset(ip)
	s.startSession(w, u.Name)
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Name})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.Sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startSession(w http.ResponseWriter, user string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.Sessions.Create(user),
		Path:     "/",
		HttpOnly: true,                    // JavaScript can't read it (limits XSS damage)
		Secure:   true,                    // only sent over HTTPS
		SameSite: http.SameSiteStrictMode, // never sent by other sites' requests
	})
}

type ctxUser struct{}

func userFrom(r *http.Request) string {
	u, _ := r.Context().Value(ctxUser{}).(string)
	return u
}

func (s *Server) requireLogin(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		user, ok := s.Sessions.Get(c.Value)
		if !ok {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		// Attach the user to the request so handlers can know who is calling.
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUser{}, user)))
	})
}

// ---- read-only endpoints ----

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"username": userFrom(r)})
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	info := system.Read()
	info.Version = s.Version
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) disks(w http.ResponseWriter, r *http.Request) {
	list, err := s.Disks.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ---- middleware ----

// csrfGuard: a malicious web page you visit could try to submit a form to
// your NAS. Browsers let pages send cross-site forms, but not with a JSON
// content type (that needs CORS permission we never grant). Requiring JSON
// on every state-changing request closes that door; SameSite=Strict
// cookies are the second lock.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && strings.HasPrefix(r.URL.Path, "/api/") {
			ct := r.Header.Get("Content-Type")
			if r.ContentLength > 0 && !strings.HasPrefix(ct, "application/json") {
				writeError(w, http.StatusUnsupportedMediaType, "requests must be JSON")
				return
			}
			if r.Header.Get("X-Requested-With") != "nasd" {
				writeError(w, http.StatusForbidden, "missing X-Requested-With header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) { r.status = code; r.ResponseWriter.WriteHeader(code) }

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		// Only what changes something, fails or is slow: the page polls GETs
		// every few seconds, and logging them all fills the journal (and
		// keeps writing to the system disks).
		took := time.Since(start)
		if r.Method != http.MethodGet || rec.status >= 400 || took > 2*time.Second {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, rec.status, took.Round(time.Millisecond))
		}
	})
}

// spaHandler serves the built UI. Unknown paths get index.html so the
// Svelte app can handle its own routes (e.g. /disks after a page reload).
func spaHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(ui, p); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built: run `make ui`", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(index)
	})
}

// ---- helpers ----

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- front display ----

func (s *Server) getLCD(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"frame": s.LCD.Current(), "driver": s.LCD.Out.Name(),
		"settings": s.Store.Read().LCD})
}

func (s *Server) setLCD(w http.ResponseWriter, r *http.Request) {
	var req store.LCDSettings
	if readJSON(w, r, &req) {
		result(w, s.Store.Update(func(st *store.State) error { st.LCD = req; return nil }))
	}
}

func (s *Server) wakeLCD(w http.ResponseWriter, r *http.Request) {
	s.LCD.Wake()
	w.WriteHeader(http.StatusNoContent)
}
