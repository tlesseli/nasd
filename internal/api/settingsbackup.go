package api

// Settings backup (see internal/confbackup):
//
//	GET  /api/settings-backup           -> status, copies found on the volumes
//	POST /api/settings-backup           {nightly, volume, passphrase?, password?}
//	                                    (a new passphrase needs the admin password)
//	POST /api/settings-backup/copy      -> writes a copy on the data volume now
//	POST /api/settings-backup/download  {password}  -> the encrypted file
//	POST /api/settings-backup/restore   {password, passphrase, volume+name | file (base64)} -> 202 job

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"readynas/internal/confbackup"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

func (s *Server) settingsBackupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/settings-backup", s.requireLogin(s.getSettingsBackup))
	mux.Handle("POST /api/settings-backup", s.requireLogin(s.setSettingsBackup))
	mux.Handle("POST /api/settings-backup/copy", s.requireLogin(s.copySettingsNow))
	mux.Handle("POST /api/settings-backup/download", s.requireLogin(s.downloadSettings))
	mux.Handle("POST /api/settings-backup/restore", s.requireLogin(s.restoreSettings))
}

func (s *Server) getSettingsBackup(w http.ResponseWriter, r *http.Request) {
	cfg := s.Store.Read().ConfigBackup
	mounts, _ := s.ConfBackup.Mounts(r.Context())
	vols := []string{}
	for v := range mounts {
		vols = append(vols, v)
	}
	sort.Strings(vols)
	writeJSON(w, http.StatusOK, map[string]any{
		"passphraseSet": cfg.Passphrase != "",
		"nightly":       cfg.Nightly,
		"volume":        cfg.Volume,
		"lastCopy":      cfg.LastCopy,
		"lastError":     cfg.LastError,
		"volumes":       vols,
		"copies":        s.ConfBackup.Copies(r.Context()),
	})
}

func (s *Server) setSettingsBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nightly    bool   `json:"nightly"`
		Volume     string `json:"volume"`
		Passphrase string `json:"passphrase"` // "" = keep the current one
		Password   string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Changing the passphrase changes who can open future copies: the
	// admin password confirms it.
	if req.Passphrase != "" {
		if len(req.Passphrase) < confbackup.MinPassphrase {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("the passphrase needs at least %d characters", confbackup.MinPassphrase))
			return
		}
		if !s.confirmAdmin(w, r, req.Password) {
			return
		}
	}
	err := s.Store.Update(func(st *store.State) error {
		if req.Passphrase != "" {
			st.ConfigBackup.Passphrase = req.Passphrase
		}
		if req.Nightly && st.ConfigBackup.Passphrase == "" {
			return errors.New("set a passphrase first")
		}
		st.ConfigBackup.Nightly, st.ConfigBackup.Volume = req.Nightly, req.Volume
		return nil
	})
	result(w, err)
}

func (s *Server) copySettingsNow(w http.ResponseWriter, r *http.Request) {
	result(w, s.ConfBackup.CopyNow(r.Context()))
}

func (s *Server) downloadSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) || !s.confirmAdmin(w, r, req.Password) {
		return
	}
	data, name, err := s.ConfBackup.Make(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

func (s *Server) restoreSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password   string `json:"password"`
		Passphrase string `json:"passphrase"`
		Volume     string `json:"volume"` // a copy on a data volume...
		Name       string `json:"name"`
		File       string `json:"file"` // ...or an uploaded file, base64
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request (a settings file is a few hundred KB at most)")
		return
	}
	if !s.confirmAdmin(w, r, req.Password) {
		return
	}
	var data []byte
	var err error
	if req.File != "" {
		data, err = base64.StdEncoding.DecodeString(req.File)
	} else {
		data, err = s.ConfBackup.ReadCopy(r.Context(), req.Volume, req.Name)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b, err := confbackup.Decrypt(data, req.Passphrase)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := fmt.Sprintf("Restore settings from %s (%s)", b.Created.Format("2006-01-02"), b.Hostname)
	s.startJob(w, title, 30*time.Minute, func(ctx context.Context, j *jobs.Job) error {
		return s.ConfBackup.Restore(ctx, j.Run, j.Logf, b, req.Passphrase)
	})
}
