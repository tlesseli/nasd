package api

// Alerts, health and volume maintenance (milestone 5).
//
//	GET  /api/alerts                       -> {settings, kinds}
//	POST /api/alerts                       store.AlertSettings
//	POST /api/alerts/test                  store.AlertSettings -> sends a test email
//	GET  /api/events                       -> recent events, newest first
//	GET  /api/sensors                      -> []health.Sensor
//	GET  /api/disks/{name}/smart           -> {report}
//	GET  /api/logs?source=&lines=&errors=  -> {lines, sources}
//	GET  /api/volumes/{name}/maintenance   -> store.Maintenance
//	POST /api/volumes/{name}/maintenance   store.Maintenance
//	POST /api/volumes/{name}/check         {action: check|repair}
//	POST /api/volumes/{name}/fscheck       -> 202 job (btrfs) or {atBoot: true} (ext4)

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"readynas/internal/alerts"
	"readynas/internal/health"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

func (s *Server) healthRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/alerts", s.requireLogin(s.getAlerts))
	mux.Handle("POST /api/alerts", s.requireLogin(s.saveAlerts))
	mux.Handle("POST /api/alerts/test", s.requireLogin(s.testAlerts))
	mux.Handle("GET /api/events", s.requireLogin(s.listEvents))
	mux.Handle("GET /api/sensors", s.requireLogin(s.sensors))
	mux.Handle("GET /api/disks/{name}/smart", s.requireLogin(s.smartReport))
	mux.Handle("GET /api/logs", s.requireLogin(s.logs))
	mux.Handle("GET /api/volumes/{name}/maintenance", s.requireLogin(s.getMaintenance))
	mux.Handle("POST /api/volumes/{name}/maintenance", s.requireLogin(s.saveMaintenance))
	mux.Handle("POST /api/volumes/{name}/check", s.requireLogin(s.checkVolume))
	mux.Handle("POST /api/volumes/{name}/fscheck", s.requireLogin(s.fsCheck))
}

func (s *Server) getAlerts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": s.Alerts.Settings(),
		"kinds":    alerts.Kinds,
		"defaults": map[string]int{"diskTempLimit": alerts.DefaultDiskTemp, "volumeFullPercent": alerts.DefaultFullPct},
	})
}

func (s *Server) saveAlerts(w http.ResponseWriter, r *http.Request) {
	var cfg store.AlertSettings
	if readJSON(w, r, &cfg) {
		result(w, s.Alerts.Save(cfg))
	}
}

func (s *Server) testAlerts(w http.ResponseWriter, r *http.Request) {
	var cfg store.AlertSettings
	if readJSON(w, r, &cfg) {
		result(w, s.Alerts.Test(r.Context(), cfg))
	}
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	ev := s.Store.Read().Events
	slices.Reverse(ev)
	if ev == nil {
		ev = []store.Event{}
	}
	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) sensors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, health.Sensors(s.Sys))
}

func (s *Server) smartReport(w http.ResponseWriter, r *http.Request) {
	report, err := health.SmartReport(r.Context(), s.Run, r.PathValue("name"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"report": report})
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	source := q.Get("source")
	if source == "" {
		source = "all"
	}
	n, _ := strconv.Atoi(q.Get("lines"))
	lines, err := health.Logs(r.Context(), s.Run, source, n, q.Get("errors") == "1")
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": lines, "sources": health.LogSources()})
}

func (s *Server) getMaintenance(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Read().Maintenance[r.PathValue("name")])
}

func (s *Server) saveMaintenance(w http.ResponseWriter, r *http.Request) {
	var req store.Maintenance
	if !readJSON(w, r, &req) {
		return
	}
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	sc := req.Scrub
	if !slices.Contains([]string{"", "daily", "weekly"}, sc.Kind) {
		writeError(w, http.StatusBadRequest, "unknown schedule")
		return
	}
	if sc.Hour < 0 || sc.Hour > 23 || sc.Minute < 0 || sc.Minute > 59 || sc.Weekday < 0 || sc.Weekday > 6 {
		writeError(w, http.StatusBadRequest, "invalid schedule time")
		return
	}
	result(w, s.Store.Update(func(st *store.State) error {
		if st.Maintenance == nil {
			st.Maintenance = map[string]store.Maintenance{}
		}
		req.FsckAtBoot = st.Maintenance[v.Name].FsckAtBoot // not settable from here
		st.Maintenance[v.Name] = req
		return nil
	}))
}

func (s *Server) checkVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	if err := s.Volumes.StartCheck(v, req.Action); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Monitor.MarkStarted(v.Name, req.Action)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fsCheck(w http.ResponseWriter, r *http.Request) {
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	if v.Filesystem == "ext4" {
		atBoot, err := s.Volumes.CheckFilesystem(r.Context(), s.Run, v)
		if err == nil && atBoot {
			err = s.Store.Update(func(st *store.State) error {
				if st.Maintenance == nil {
					st.Maintenance = map[string]store.Maintenance{}
				}
				m := st.Maintenance[v.Name]
				m.FsckAtBoot = true
				st.Maintenance[v.Name] = m
				return nil
			})
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"atBoot": true})
		return
	}
	s.startJob(w, "Check filesystem of "+v.Name, 24*time.Hour, func(ctx context.Context, j *jobs.Job) error {
		_, err := s.Volumes.CheckFilesystem(ctx, j.Run, v)
		if err != nil {
			s.Alerts.Notify("scrub_mismatch", "Filesystem check of "+v.Name+" found errors", err.Error())
			return errors.New("btrfs scrub reported errors: see the log above")
		}
		return nil
	})
}
