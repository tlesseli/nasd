package api

// Hardware: Backup button, locate a disk, fan control (milestone 6 prep).
//
//	GET  /api/hardware                   -> button, fan, outputs, locating
//	POST /api/button                     store.ButtonSettings
//	POST /api/button/learn               waits up to 30 s for a press -> {device, code}
//	POST /api/button/press               simulates a press
//	POST /api/fan                        store.FanSettings
//	POST /api/disks/{name}/locate        {seconds}
//	POST /api/disks/{name}/locate/stop

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"readynas/internal/store"
)

func (s *Server) hardwareRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/hardware", s.requireLogin(s.getHardware))
	mux.Handle("POST /api/button", s.requireLogin(s.saveButton))
	mux.Handle("POST /api/button/learn", s.requireLogin(s.learnButton))
	mux.Handle("POST /api/button/press", s.requireLogin(s.pressButton))
	mux.Handle("POST /api/fan", s.requireLogin(s.saveFan))
	mux.Handle("POST /api/disks/{name}/locate", s.requireLogin(s.locateDisk))
	mux.Handle("POST /api/disks/{name}/locate/stop", s.requireLogin(s.stopLocate))
}

func (s *Server) getHardware(w http.ResponseWriter, r *http.Request) {
	st := s.Store.Read()
	if st.Button.Jobs == nil {
		st.Button.Jobs = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"button":        st.Button,
		"buttonRunning": s.Button.Running(),
		"fan":           st.Fan,
		"fanStatus":     s.Fan.Status(),
		"outputs":       s.Fan.Outputs(),
		"locating":      s.Locator.Active(),
	})
}

func (s *Server) saveButton(w http.ResponseWriter, r *http.Request) {
	var b store.ButtonSettings
	if !readJSON(w, r, &b) {
		return
	}
	result(w, s.Store.Update(func(st *store.State) error {
		for _, id := range b.Jobs {
			if st.FindBackup(id) == nil {
				return fmt.Errorf("backup job %d not found", id)
			}
		}
		st.Button = b
		return nil
	}))
}

func (s *Server) learnButton(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	k, err := s.Button.Learn(ctx)
	if err != nil {
		writeError(w, http.StatusRequestTimeout, err.Error()+" within 30 seconds")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) pressButton(w http.ResponseWriter, r *http.Request) {
	result(w, s.Button.Press("simulated from the web interface"))
}

func (s *Server) saveFan(w http.ResponseWriter, r *http.Request) {
	var c store.FanSettings
	if readJSON(w, r, &c) {
		result(w, s.Fan.Save(r.Context(), c))
	}
}

func (s *Server) locateDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Seconds int `json:"seconds"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	d := time.Duration(min(max(req.Seconds, 10), 600)) * time.Second
	result(w, s.Locator.Start(r.PathValue("name"), d))
}

func (s *Server) stopLocate(w http.ResponseWriter, r *http.Request) {
	s.Locator.Stop(r.PathValue("name"))
	w.WriteHeader(http.StatusNoContent)
}
