package api

// Data for the Overview (redesign).
//
//	GET  /api/metrics?range=1h|24h|7d|30d  -> {step, points}
//	GET  /api/storage/history              -> hourly bytes used per volume (90 days)
//	GET  /api/attention                    -> current problems, most severe first
//	GET  /api/security                     -> failed sign-ins in the last 24 h
//	POST /api/disks/{name}/selftest        -> starts a SMART short self-test

import (
	"net/http"
	"sync"
	"time"
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
	result(w, s.Disks.SelfTest(r.Context(), r.PathValue("name")))
}
