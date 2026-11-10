package api

// Power & system settings (milestone 5c).
//
//	GET  /api/power             -> {settings, ups, nics, choices}
//	POST /api/power             store.PowerSettings
//	GET  /api/network           -> sysconf.NetworkInfo
//	POST /api/network           store.NetworkSettings -> {deadline}  (must be confirmed)
//	POST /api/network/confirm
//	POST /api/hostname          {name}            (nasd restarts)
//	GET  /api/time              -> {info, timezones}
//	POST /api/time              sysconf.TimeRequest
//	POST /api/system/power      {action: reboot|poweroff, force}
//	GET  /api/updates           -> sysconf.UpdatesInfo
//	POST /api/updates/check     -> 202 job
//	POST /api/updates/install   -> 202 job
//	POST /api/updates/auto      store.UpdateSettings (automatic security updates)

import (
	"context"
	"net/http"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/power"
	"readynas/internal/store"
	"readynas/internal/sysconf"
)

func (s *Server) systemRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/power", s.requireLogin(s.getPower))
	mux.Handle("POST /api/power", s.requireLogin(s.savePower))
	mux.Handle("GET /api/network", s.requireLogin(s.getNetwork))
	mux.Handle("POST /api/network", s.requireLogin(s.setNetwork))
	mux.Handle("POST /api/network/confirm", s.requireLogin(s.confirmNetwork))
	mux.Handle("POST /api/hostname", s.requireLogin(s.setHostname))
	mux.Handle("GET /api/time", s.requireLogin(s.getTime))
	mux.Handle("POST /api/time", s.requireLogin(s.setTime))
	mux.Handle("POST /api/system/power", s.requireLogin(s.systemPower))
	mux.Handle("GET /api/updates", s.requireLogin(s.getUpdates))
	mux.Handle("POST /api/updates/check", s.requireLogin(s.checkUpdates))
	mux.Handle("POST /api/updates/install", s.requireLogin(s.installUpdates))
	mux.Handle("POST /api/updates/auto", s.requireLogin(s.setAutoUpdates))
}

func (s *Server) getPower(w http.ResponseWriter, r *http.Request) {
	st := s.Store.Read()
	var nextWake *time.Time
	if st.Power.Timer.Off.Kind != "" {
		t := power.NextWake(st.Power.Timer, time.Now())
		nextWake = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":        st.Power,
		"ups":             s.Power.UPSStatus(r.Context()),
		"nics":            s.Power.NICs(r.Context()),
		"spinDownChoices": power.SpinDownChoices,
		"upsDrivers":      power.UPSDrivers,
		"nextWake":        nextWake,
	})
}

func (s *Server) savePower(w http.ResponseWriter, r *http.Request) {
	var p store.PowerSettings
	if readJSON(w, r, &p) {
		result(w, s.Power.Save(r.Context(), p))
	}
}

func (s *Server) getNetwork(w http.ResponseWriter, r *http.Request) {
	info := s.System.Network(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"info": info, "bondModes": sysconf.BondModes})
}

func (s *Server) setNetwork(w http.ResponseWriter, r *http.Request) {
	var n store.NetworkSettings
	if !readJSON(w, r, &n) {
		return
	}
	deadline, err := s.System.SetNetwork(r.Context(), n)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]time.Time{"deadline": deadline})
}

func (s *Server) confirmNetwork(w http.ResponseWriter, r *http.Request) {
	result(w, s.System.ConfirmNetwork())
}

func (s *Server) setHostname(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if readJSON(w, r, &req) {
		result(w, s.System.SetHostname(r.Context(), req.Name))
	}
}

func (s *Server) getTime(w http.ResponseWriter, r *http.Request) {
	info, err := s.System.Time(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	zones, _ := s.System.Timezones(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"info": info, "timezones": zones})
}

func (s *Server) setTime(w http.ResponseWriter, r *http.Request) {
	var req sysconf.TimeRequest
	if readJSON(w, r, &req) {
		result(w, s.System.SetTime(r.Context(), req))
	}
}

func (s *Server) systemPower(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		Force  bool   `json:"force"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err := s.System.Power(r.Context(), req.Action, req.Force)
	if err == nil && s.LCD != nil {
		s.LCD.SetPhase(map[string]string{"reboot": "Restarting...", "poweroff": "Shutting down"}[req.Action], "please wait")
	}
	result(w, err)
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request) {
	info, err := s.System.Updates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) checkUpdates(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "Check for updates", 30*time.Minute, func(ctx context.Context, j *jobs.Job) error {
		return s.System.CheckUpdates(ctx, j.Run)
	})
}

func (s *Server) installUpdates(w http.ResponseWriter, r *http.Request) {
	s.startJob(w, "Install updates", 2*time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.System.InstallUpdates(ctx, j.Run)
	})
}

func (s *Server) setAutoUpdates(w http.ResponseWriter, r *http.Request) {
	var req store.UpdateSettings
	if readJSON(w, r, &req) {
		result(w, s.System.SaveAutoUpdates(req))
	}
}
