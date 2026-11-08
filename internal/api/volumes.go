package api

// Volume endpoints:
//
//	GET  /api/volumes                    -> []volumes.Volume
//	POST /api/volumes                    {name, level, filesystem, disks[]}   -> 202 job
//	POST /api/volumes/{name}/delete      {confirm: "<name>"}                  -> 202 job
//	POST /api/volumes/{name}/add         {disk}                               -> 202 job
//	POST /api/volumes/{name}/remove      {disk}                               -> 202 job
//	POST /api/volumes/{name}/expand      {disk}  (free disk or current spare)  -> 202 job
//	POST /api/volumes/{name}/grow-fs                                          -> 202 job
//	POST /api/disks/{name}/erase         {confirm: "<disk name>"}             -> 202 job
//	GET  /api/jobs                       -> recent jobs, newest first
//	GET  /api/jobs/{id}                  -> one job with its log
//
// Every handler validates first (fast, answers 400 with a clear message)
// and only then starts the job that touches the disks.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/volumes"
)

func (s *Server) volumeRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/volumes", s.requireLogin(s.listVolumes))
	mux.Handle("POST /api/volumes", s.requireLogin(s.createVolume))
	mux.Handle("POST /api/volumes/{name}/delete", s.requireLogin(s.deleteVolume))
	mux.Handle("POST /api/volumes/{name}/add", s.requireLogin(s.addDisk))
	mux.Handle("POST /api/volumes/{name}/remove", s.requireLogin(s.removeDisk))
	mux.Handle("POST /api/volumes/{name}/expand", s.requireLogin(s.expandVolume))
	mux.Handle("POST /api/volumes/{name}/grow-fs", s.requireLogin(s.growFS))
	mux.Handle("POST /api/disks/{name}/erase", s.requireLogin(s.eraseDisk))
	mux.Handle("GET /api/jobs", s.requireLogin(s.listJobs))
	mux.Handle("GET /api/jobs/{id}", s.requireLogin(s.getJob))
}

func (s *Server) listVolumes(w http.ResponseWriter, r *http.Request) {
	vols, err := s.Volumes.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vols)
}

// inventory fetches disks and volumes together, as most validations need both.
func (s *Server) inventory(ctx context.Context) ([]disks.Disk, []volumes.Volume, error) {
	all, err := s.Disks.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	vols, err := s.Volumes.List(ctx)
	return all, vols, err
}

func (s *Server) createVolume(w http.ResponseWriter, r *http.Request) {
	var req volumes.CreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	all, vols, err := s.inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	selected, err := s.Volumes.ValidateCreate(req, all, vols)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	title := fmt.Sprintf("Create volume %s (%s, %d disks, %s)", req.Name, req.Level, len(selected), req.Filesystem)
	s.startJob(w, title, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.Create(ctx, j.Run, req, selected)
	})
}

func (s *Server) deleteVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	// Typing the name is the "are you really sure" step, enforced server side.
	if req.Confirm != v.Name {
		writeError(w, http.StatusBadRequest, "confirmation does not match the volume name")
		return
	}
	if names := s.Shares.OnVolume(v.Name); len(names) > 0 {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("volume %s holds shares (%s): delete them first", v.Name, strings.Join(names, ", ")))
		return
	}
	s.startJob(w, "Delete volume "+v.Name, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.Delete(ctx, j.Run, v)
	})
}

func (s *Server) addDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk string `json:"disk"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	all, vols, err := s.inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := pick(w, vols, r.PathValue("name"))
	if !ok {
		return
	}
	d, err := volumes.ValidateAddDisk(v, req.Disk, all, vols)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.startJob(w, fmt.Sprintf("Add %s to volume %s", d.Name, v.Name), time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.AddDisk(ctx, j.Run, v, d)
	})
}

func (s *Server) removeDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk string `json:"disk"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	m, err := volumes.ValidateRemoveDisk(v, req.Disk)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.startJob(w, fmt.Sprintf("Remove %s from volume %s", m.Disk, v.Name), time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.RemoveDisk(ctx, j.Run, v, m)
	})
}

func (s *Server) expandVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk string `json:"disk"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	all, vols, err := s.inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	v, ok := pick(w, vols, r.PathValue("name"))
	if !ok {
		return
	}
	d, err := volumes.ValidateExpand(v, req.Disk, all, vols)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A reshape rewrites the whole volume: allow it up to 3 days.
	title := fmt.Sprintf("Expand volume %s with %s (%d → %d disks)", v.Name, req.Disk, v.RaidDisks, v.RaidDisks+1)
	s.startJob(w, title, 72*time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.Expand(ctx, j.Run, j.Logf, v, d)
	})
}

func (s *Server) growFS(w http.ResponseWriter, r *http.Request) {
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	if v.Sync != nil {
		writeError(w, http.StatusBadRequest, "wait for the current "+v.Sync.Action+" to finish")
		return
	}
	s.startJob(w, "Grow filesystem of "+v.Name, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.GrowFS(ctx, j.Run, v)
	})
}

func (s *Server) eraseDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm string `json:"confirm"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := r.PathValue("name")
	if req.Confirm != name {
		writeError(w, http.StatusBadRequest, "confirmation does not match the disk name")
		return
	}
	all, vols, err := s.inventory(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	d, err := volumes.ValidateErase(name, all, vols)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.startJob(w, "Erase disk "+d.Name, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		return s.Volumes.EraseDisk(ctx, j.Run, d)
	})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Jobs.List())
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	j, ok := s.Jobs.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// ---- helpers ----

func (s *Server) startJob(w http.ResponseWriter, title string, timeout time.Duration, fn func(context.Context, *jobs.Job) error) {
	snap, err := s.Jobs.Start(title, timeout, fn)
	if errors.Is(err, jobs.ErrBusy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, snap)
}

func (s *Server) findVolume(w http.ResponseWriter, r *http.Request) (volumes.Volume, bool) {
	vols, err := s.Volumes.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return volumes.Volume{}, false
	}
	return pick(w, vols, r.PathValue("name"))
}

func pick(w http.ResponseWriter, vols []volumes.Volume, name string) (volumes.Volume, bool) {
	for _, v := range vols {
		if v.Name == name {
			return v, true
		}
	}
	writeError(w, http.StatusNotFound, fmt.Sprintf("volume %q not found", name))
	return volumes.Volume{}, false
}
