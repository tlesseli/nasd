package api

// Users & shares endpoints (milestone 4). These are quick operations, so
// they answer directly instead of starting background jobs.
//
//	GET  /api/users                          -> []shares.UserInfo
//	POST /api/users                          {name, password}
//	POST /api/users/{name}/password          {password}
//	POST /api/users/{name}/delete
//	GET  /api/shares                         -> []shares.ShareInfo
//	POST /api/shares                         shares.ShareRequest
//	POST /api/shares/{name}                  shares.ShareRequest (settings only)
//	POST /api/shares/{name}/delete           (files are kept)
//	GET  /api/services                       -> smbd / wsdd2 / avahi status
//	POST /api/me/password                    {current, new}  admin password

import (
	"fmt"
	"net/http"
	"strings"

	"readynas/internal/auth"
	"readynas/internal/backups"
	"readynas/internal/shares"
	"readynas/internal/store"
)

func (s *Server) shareRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/users", s.requireLogin(s.listUsers))
	mux.Handle("POST /api/users", s.requireLogin(s.createUser))
	mux.Handle("POST /api/users/{name}/password", s.requireLogin(s.setUserPassword))
	mux.Handle("POST /api/users/{name}/delete", s.requireLogin(s.deleteUser))
	mux.Handle("GET /api/shares", s.requireLogin(s.listShares))
	mux.Handle("POST /api/shares", s.requireLogin(s.createShare))
	mux.Handle("GET /api/shares/unshared", s.requireLogin(s.unsharedFolders))
	mux.Handle("POST /api/shares/{name}", s.requireLogin(s.updateShare))
	mux.Handle("POST /api/shares/{name}/delete", s.requireLogin(s.deleteShare))
	mux.Handle("GET /api/services", s.requireLogin(s.services))
	mux.Handle("POST /api/me/password", s.requireLogin(s.changeAdminPassword))
}

// result writes 204 on success or the error as a 400 (validation and
// command errors are both meant to be shown to the user).
func result(w http.ResponseWriter, err error) {
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Shares.Users())
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if readJSON(w, r, &req) {
		result(w, s.Shares.CreateUser(r.Context(), req.Name, req.Password))
	}
}

func (s *Server) setUserPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
	}
	if readJSON(w, r, &req) {
		result(w, s.Shares.SetPassword(r.Context(), r.PathValue("name"), req.Password))
	}
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	result(w, s.Shares.DeleteUser(r.Context(), r.PathValue("name")))
}

func (s *Server) listShares(w http.ResponseWriter, r *http.Request) {
	list, err := s.Shares.Shares(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// unsharedFolders: folders already on the volumes that no share uses.
func (s *Server) unsharedFolders(w http.ResponseWriter, r *http.Request) {
	vols, err := s.Volumes.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	mounts := map[string]string{}
	for _, v := range vols {
		if v.Managed && v.Mountpoint != "" {
			mounts[v.Name] = v.Mountpoint
		}
	}
	writeJSON(w, http.StatusOK, s.Shares.Unshared(mounts))
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request) {
	var req shares.ShareRequest
	if readJSON(w, r, &req) {
		result(w, s.Shares.CreateShare(r.Context(), req))
	}
}

func (s *Server) updateShare(w http.ResponseWriter, r *http.Request) {
	var req shares.ShareRequest
	if readJSON(w, r, &req) {
		result(w, s.Shares.UpdateShare(r.Context(), r.PathValue("name"), req))
	}
}

func (s *Server) deleteShare(w http.ResponseWriter, r *http.Request) {
	st := s.Store.Read()
	if used := backups.SharesUsedBy(&st, r.PathValue("name")); len(used) > 0 {
		writeError(w, http.StatusBadRequest, "backup jobs use this share ("+strings.Join(used, ", ")+"): delete them first")
		return
	}
	result(w, s.Shares.DeleteShare(r.Context(), r.PathValue("name")))
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	type svc struct {
		shares.ServiceStatus
		Detail string `json:"detail"`
	}
	var out []svc
	for _, st := range s.Shares.Status(r.Context()) {
		x := svc{ServiceStatus: st}
		if st.Unit == "smbd" && st.Active {
			x.Detail = fmt.Sprintf("%d sessions", s.Shares.SMBSessions(r.Context()))
		}
		out = append(out, x)
	}
	if s.Power != nil {
		if u := s.Power.UPSStatus(r.Context()); u.Enabled {
			x := svc{ServiceStatus: shares.ServiceStatus{Unit: "ups", Label: "UPS", Active: u.Error == ""}}
			switch {
			case u.Error != "":
				x.Detail = "not reachable"
			case u.OnBattery:
				x.Detail = fmt.Sprintf("on battery · %d%%", u.Charge)
			default:
				x.Detail = fmt.Sprintf("battery %d%%", u.Charge)
			}
			out = append(out, x)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) changeAdminPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	name := userFrom(r)
	st := s.Store.Read()
	u := st.FindUser(name)
	if u == nil || !auth.CheckPassword(u.PasswordHash, req.Current) {
		writeError(w, http.StatusBadRequest, "current password is wrong")
		return
	}
	if len(req.New) < minPasswordLength {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	err = s.Store.Update(func(st *store.State) error {
		st.FindUser(name).PasswordHash = hash
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Log out other sessions (e.g. a forgotten browser), keep this one.
	s.Sessions.DeleteUser(name)
	s.startSession(w, name)
	w.WriteHeader(http.StatusNoContent)
}
