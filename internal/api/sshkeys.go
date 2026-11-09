package api

// SSH access for the administrator account of the system ("nas", with sudo).
// A distributed image ships without any key (it would open every copy to
// whoever holds it): the owner adds their own public key here.
//
//	GET  /api/ssh/keys   -> {user, keys: ["ssh-ed25519 AAAA... comment", ...]}
//	POST /api/ssh/keys   {keys: [...]}  (replaces the list; admin password)

import (
	"errors"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const sshAdminUser = "nas"

func (s *Server) sshRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/ssh/keys", s.requireLogin(s.getSSHKeys))
	mux.Handle("POST /api/ssh/keys", s.requireLogin(s.setSSHKeys))
}

func authorizedKeysPath() (string, *user.User, error) {
	u, err := user.Lookup(sshAdminUser)
	if err != nil {
		return "", nil, errors.New("this system has no " + sshAdminUser + " account")
	}
	return filepath.Join(u.HomeDir, ".ssh", "authorized_keys"), u, nil
}

func (s *Server) getSSHKeys(w http.ResponseWriter, r *http.Request) {
	path, _, err := authorizedKeysPath()
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	data, _ := os.ReadFile(path)
	keys := []string{}
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			keys = append(keys, l)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": sshAdminUser, "keys": keys})
}

func (s *Server) setSSHKeys(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys     []string `json:"keys"`
		Password string   `json:"password"`
	}
	if !readJSON(w, r, &req) || !s.confirmAdmin(w, r, req.Password) {
		return
	}
	path, u, err := authorizedKeysPath()
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var keys []string
	for _, k := range req.Keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		// ssh-keygen -l reads the key and fails on anything that isn't one
		// (a private key pasted by mistake, options, garbage).
		cmd := exec.CommandContext(r.Context(), "ssh-keygen", "-l", "-f", "-")
		cmd.Stdin = strings.NewReader(k + "\n")
		if strings.Contains(k, "PRIVATE KEY") {
			// Never echo it back: paste the .pub file instead.
			writeError(w, http.StatusBadRequest, "this is a private key: paste the public one (the .pub file) instead, and keep the private key secret")
			return
		}
		if strings.ContainsAny(k, "\r\n") || cmd.Run() != nil {
			writeError(w, http.StatusBadRequest, "not a public SSH key: "+abbreviate(k))
			return
		}
		keys = append(keys, k)
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	data := strings.Join(keys, "\n")
	if data != "" {
		data += "\n"
	}
	// sshd ignores the file unless only its owner can write it.
	if err := os.WriteFile(path+".new", []byte(data), 0o600); err == nil {
		os.Chown(path+".new", uid, gid)
		os.Chown(dir, uid, gid)
		err = os.Rename(path+".new", path)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	} else {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.Alerts != nil {
		s.Alerts.Notify("ssh_keys", "Administrator SSH keys changed", strconv.Itoa(len(keys))+" key(s) now allowed, set from the web interface ("+clientIP(r)+").")
	}
	w.WriteHeader(http.StatusNoContent)
}

func abbreviate(k string) string {
	if len(k) > 40 {
		return k[:40] + "…"
	}
	return k
}
