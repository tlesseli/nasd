package api

// Home shares, quotas and USB copy (milestone 5d).
//
//	GET  /api/homes                    -> store.HomeShares
//	POST /api/homes                    {enabled, volume}
//	POST /api/users/{name}/quota       {quotaMB}
//	GET  /api/quotas                   -> per volume: quotas on?, usage per user
//	POST /api/volumes/{name}/quotas    -> 202 job (enable quotas: briefly unmounts)
//	GET  /api/usbcopy                  -> store.USBCopy
//	POST /api/usbcopy                  store.USBCopy

import (
	"context"
	"net/http"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
	"readynas/internal/volumes"
)

func (s *Server) users2Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/homes", s.requireLogin(s.getHomes))
	mux.Handle("POST /api/homes", s.requireLogin(s.setHomes))
	mux.Handle("POST /api/users/{name}/quota", s.requireLogin(s.setQuota))
	mux.Handle("GET /api/quotas", s.requireLogin(s.quotas))
	mux.Handle("POST /api/volumes/{name}/quotas", s.requireLogin(s.enableQuotas))
	mux.Handle("GET /api/usbcopy", s.requireLogin(s.getUSBCopy))
	mux.Handle("POST /api/usbcopy", s.requireLogin(s.setUSBCopy))
}

func (s *Server) getHomes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Read().Settings.HomeShares)
}

func (s *Server) setHomes(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool   `json:"enabled"`
		Volume  string `json:"volume"`
	}
	if readJSON(w, r, &req) {
		result(w, s.Shares.SetHomeShares(r.Context(), req.Enabled, req.Volume))
	}
}

func (s *Server) setQuota(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaMB int `json:"quotaMB"`
	}
	if readJSON(w, r, &req) {
		result(w, s.Shares.SetQuota(r.Context(), r.PathValue("name"), req.QuotaMB))
	}
}

type volumeQuotas struct {
	Volume     string             `json:"volume"`
	Filesystem string             `json:"filesystem"`
	Enabled    bool               `json:"enabled"`
	Usage      []volumes.QuotaUse `json:"usage"`
}

func (s *Server) quotas(w http.ResponseWriter, r *http.Request) {
	vols, err := s.Volumes.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []volumeQuotas{}
	for _, v := range vols {
		if v.Mountpoint == "" {
			continue
		}
		q := volumeQuotas{Volume: v.Name, Filesystem: v.Filesystem, Enabled: v.Quotas, Usage: []volumes.QuotaUse{}}
		if v.Quotas {
			if usage, err := s.Volumes.QuotaUsage(r.Context(), v); err == nil {
				q.Usage = usage
			}
		}
		out = append(out, q)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) enableQuotas(w http.ResponseWriter, r *http.Request) {
	v, ok := s.findVolume(w, r)
	if !ok {
		return
	}
	if v.Filesystem != "ext4" {
		writeError(w, http.StatusBadRequest, "per-user quotas need an ext4 volume (btrfs doesn't support them)")
		return
	}
	s.startJob(w, "Enable quotas on "+v.Name, time.Hour, func(ctx context.Context, j *jobs.Job) error {
		if err := s.Volumes.EnableQuotas(ctx, j.Run, v); err != nil {
			return err
		}
		time.Sleep(time.Second) // let the remount settle before setting limits
		j.Logf("Applying user quotas")
		return s.Shares.ApplyQuotas(ctx)
	})
}

func (s *Server) getUSBCopy(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Store.Read().Settings.USBCopy)
}

func (s *Server) setUSBCopy(w http.ResponseWriter, r *http.Request) {
	var c store.USBCopy
	if readJSON(w, r, &c) {
		result(w, s.Backups.SaveUSBCopy(c))
	}
}
