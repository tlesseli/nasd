package api

// Data for the Overview (redesign).
//
//	GET  /api/metrics?range=1h|24h|7d|30d  -> {step, points}
//	GET  /api/storage/history              -> hourly bytes used per volume (90 days)
//	GET  /api/attention                    -> current problems, most severe first
//	GET  /api/security                     -> failed sign-ins in the last 24 h
//	POST /api/disks/{name}/selftest        {kind: short|long} -> starts a SMART self-test
//	GET  /api/disks/tests                  -> {schedule}: scheduled surface tests
//	POST /api/disks/tests                  {schedule}

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"readynas/internal/store"
)

// failedLogins remembers when sign-ins failed (for the Users page).
type failedLogins struct {
	mu    sync.Mutex
	times []time.Time
}

func (f *failedLogins) add() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.times = append(f.times, time.Now())
	if len(f.times) > 1000 {
		f.times = f.times[len(f.times)-1000:]
	}
}

func (f *failedLogins) since(d time.Duration) (int, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, last := 0, time.Time{}
	for _, t := range f.times {
		if time.Since(t) < d {
			n++
			last = t
		}
	}
	return n, last
}

func (s *Server) overviewRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/metrics", s.requireLogin(s.getMetrics))
	mux.Handle("GET /api/storage/history", s.requireLogin(s.storageHistory))
	mux.Handle("GET /api/attention", s.requireLogin(s.attention))
	mux.Handle("GET /api/security", s.requireLogin(s.security))
	mux.Handle("POST /api/disks/{name}/selftest", s.requireLogin(s.selfTest))
	mux.Handle("GET /api/disks/tests", s.requireLogin(s.getDiskTests))
	mux.Handle("POST /api/disks/tests", s.requireLogin(s.setDiskTests))
}

func (s *Server) getDiskTests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"schedule": s.Store.Read().DiskTests.Schedule})
}

// setDiskTests saves when every disk runs its SMART extended test: never,
// weekly or monthly (a long test takes hours: more often is wasteful).
func (s *Server) setDiskTests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Schedule store.Schedule `json:"schedule"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	sc := req.Schedule
	err := func() error {
		switch {
		case sc.Kind != "" && sc.Kind != "weekly" && sc.Kind != "monthly":
			return errors.New("schedule must be none, weekly or monthly")
		case sc.Hour < 0 || sc.Hour > 23 || sc.Minute < 0 || sc.Minute > 59:
			return errors.New("invalid time")
		case sc.Kind == "weekly" && (sc.Weekday < 0 || sc.Weekday > 6):
			return errors.New("invalid day of the week")
		case sc.Kind == "monthly" && (sc.Day < 1 || sc.Day > 28):
			return errors.New("day of the month must be 1 to 28")
		}
		return s.Store.Update(func(st *store.State) error { st.DiskTests.Schedule = sc; return nil })
	}()
	result(w, err)
}

func (s *Server) getMetrics(w http.ResponseWriter, r *http.Request) {
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "24h"
	}
	points, step, ok := s.Metrics.Range(rng)
	if !ok {
		writeError(w, http.StatusBadRequest, "range must be 1h, 24h, 7d or 30d")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"step": step, "points": points})
}

func (s *Server) storageHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Metrics.StorageHistory())
}

func (s *Server) attention(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Monitor.Active())
}

func (s *Server) security(w http.ResponseWriter, r *http.Request) {
	n, last := s.failed.since(24 * time.Hour)
	writeJSON(w, http.StatusOK, map[string]any{"failedSignIns24h": n, "lastFailed": last,
		"minPasswordLength": minPasswordLength, "lockoutTries": 5, "lockoutMinutes": 10, "sessionMinutes": 30})
}

func (s *Server) selfTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind string `json:"kind"` // "short" (default) or "long"
	}
	if r.ContentLength > 0 && !readJSON(w, r, &req) {
		return
	}
	if req.Kind == "" {
		req.Kind = "short"
	}
	result(w, s.Disks.SelfTest(r.Context(), r.PathValue("name"), req.Kind))
}
