package api

// Firewall (see internal/firewall):
//
//	GET  /api/firewall          -> firewall.Status
//	POST /api/firewall          store.FirewallSettings -> {deadline} (must be kept)
//	POST /api/firewall/keep     keeps the pending change
//	POST /api/firewall/undo     puts the previous settings back now

import (
	"errors"
	"net/http"

	"readynas/internal/firewall"
	"readynas/internal/store"
)

func (s *Server) firewallRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/firewall", s.requireLogin(s.getFirewall))
	mux.Handle("POST /api/firewall", s.requireLogin(s.setFirewall))
	mux.Handle("POST /api/firewall/keep", s.requireLogin(s.keepFirewall))
	mux.Handle("POST /api/firewall/undo", s.requireLogin(s.undoFirewall))
}

func (s *Server) getFirewall(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Firewall.Status(r.Context(), clientIP(r)))
}

func (s *Server) setFirewall(w http.ResponseWriter, r *http.Request) {
	var f store.FirewallSettings
	if !readJSON(w, r, &f) {
		return
	}
	deadline, err := s.Firewall.Set(r.Context(), f, clientIP(r))
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, firewall.ErrLockout) {
			code = http.StatusConflict
		}
		writeError(w, code, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deadline": deadline})
}

func (s *Server) keepFirewall(w http.ResponseWriter, r *http.Request) {
	result(w, s.Firewall.Keep(r.Context()))
}

func (s *Server) undoFirewall(w http.ResponseWriter, r *http.Request) {
	result(w, s.Firewall.Undo(r.Context()))
}
