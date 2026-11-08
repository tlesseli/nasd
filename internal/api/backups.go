package api

// Backups, USB disks and protocol settings (milestone 5b).
//
//	GET  /api/backups                 -> []store.BackupJob (passwords removed)
//	POST /api/backups                 store.BackupJob (id 0 = new)   -> {id}
//	POST /api/backups/{id}/delete
//	POST /api/backups/{id}/run        -> 202 job
//	POST /api/backups/test            store.BackupTarget (+ id to reuse a stored password) -> {message}
//	GET  /api/backups/key             -> {publicKey}
//	GET  /api/usb                     -> []backups.USBDisk
//	POST /api/usb/{disk}/format       {fstype, label, confirm} -> 202 job
//	GET  /api/settings                -> store.Settings
//	POST /api/settings                store.Settings

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"readynas/internal/backups"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

func (s *Server) backupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/backups", s.requireLogin(s.listBackups))
	mux.Handle("POST /api/backups", s.requireLogin(s.saveBackup))
	mux.Handle("POST /api/backups/test", s.requireLogin(s.testBackup))
	mux.Handle("GET /api/backups/key", s.requireLogin(s.backupKey))
	mux.Handle("POST /api/backups/{id}/delete", s.requireLogin(s.deleteBackup))
	mux.Handle("POST /api/backups/{id}/run", s.requireLogin(s.runBackup))
	mux.Handle("GET /api/usb", s.requireLogin(s.listUSB))
	mux.Handle("POST /api/usb/{disk}/format", s.requireLogin(s.formatUSB))
	mux.Handle("GET /api/settings", s.requireLogin(s.getSettings))
	mux.Handle("POST /api/settings", s.requireLogin(s.setSettings))
}

func pathID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
	}
	return id, err == nil
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Backups.List())
}

func (s *Server) saveBackup(w http.ResponseWriter, r *http.Request) {
	var b store.BackupJob
	if !readJSON(w, r, &b) {
		return
	}
	id, err := s.Backups.Save(b)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"id": id})
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if id, ok := pathID(w, r); ok {
		result(w, s.Backups.Delete(id))
	}
}

func (s *Server) runBackup(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	snap, err := s.Backups.Start(id, "manual")
	if err == jobs.ErrBusy {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, snap)
}

func (s *Server) testBackup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID int `json:"id"`
		store.BackupTarget
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Same normalization (default ports...) and checks as when saving.
	t := req.BackupTarget
	if err := backups.ValidateTarget(&t, "test"); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	st := s.Store.Read()
	stored := ""
	if b := st.FindBackup(req.ID); b != nil {
		stored = b.Target.Password
	}
	msg, err := s.Backups.Test(r.Context(), t, stored)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Server) backupKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.Backups.PublicKey(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": key})
}

func (s *Server) listUSB(w http.ResponseWriter, r *http.Request) {
	list, err := s.Backups.USBDisks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

var labelRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,11}$`) // exFAT labels are short

func (s *Server) formatUSB(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FSType  string `json:"fstype"`
		Label   string `json:"label"`
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	disk := r.PathValue("disk")
	if req.Confirm != disk {
		writeError(w, http.StatusBadRequest, "confirmation does not match the disk name")
		return
	}
	if !labelRe.MatchString(req.Label) {
		writeError(w, http.StatusBadRequest, "label: 1-11 letters, digits, - or _")
		return
	}
	// Only a disk that is actually a USB disk: never a RAID member or the system.
	list, err := s.Backups.USBDisks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	for _, d := range list {
		found = found || d.Disk == disk
	}
	if !found {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s is not a USB disk", disk))
		return
	}
	s.startJob(w, fmt.Sprintf("Format USB disk %s (%s)", disk, req.FSType), time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Backups.FormatUSB(ctx, j.Run, disk, req.FSType, req.Label)
	})
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Read().Settings)
}

func (s *Server) setSettings(w http.ResponseWriter, r *http.Request) {
	var req store.Settings
	if readJSON(w, r, &req) {
		result(w, s.Shares.SetSettings(r.Context(), req))
	}
}
