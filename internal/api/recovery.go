package api

// Lost admin password: holding the NAS's Backup button sets a new random
// one and shows it on the front display (see cmd/nasd/panel.go). nasd only
// keeps a hash of passwords, so the old one can't be shown: it is replaced.

import (
	"crypto/rand"
	"errors"
	"log"
	"net/http"

	"readynas/internal/auth"
	"readynas/internal/store"
)

// ResetAdminPassword sets a new random admin password and returns it.
// Every admin session is closed; an alert is sent.
func (s *Server) ResetAdminPassword() (string, error) {
	pw := randomPassword()
	hash, err := auth.HashPassword(pw)
	if err != nil {
		return "", err
	}
	err = s.Store.Update(func(st *store.State) error {
		u := st.FindUser(adminUser)
		if u == nil {
			return errors.New("no admin yet")
		}
		u.PasswordHash = hash
		return nil
	})
	if err != nil {
		return "", err
	}
	if s.Sessions != nil {
		s.Sessions.DeleteUser(adminUser)
	}
	if s.Throttle != nil {
		s.Throttle.ResetAll()
	}
	log.Printf("admin password reset with the Backup button")
	if s.Alerts != nil {
		s.Alerts.Notify("admin_reset", "The admin password was reset at the NAS",
			"Someone held the Backup button on the NAS for 10 seconds: the admin password was replaced by a new one, shown on the front display. "+
				"If it wasn't you, someone has physical access to your NAS.")
	}
	return pw, nil
}

// RandomCode: a setup code like "k7m2-x9qa" (no look-alike characters).
func RandomCode() string {
	pw := randomPassword()
	return pw[:4] + "-" + pw[6:10]
}

// randomPassword: 10 characters without look-alikes (no 0/o, 1/l/i), shown
// as two groups of five so it is easy to read on the display: "k7m2x-9qap4".
func randomPassword() string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 10)
	rand.Read(b)
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, chars[int(c)%len(chars)])
	}
	return string(out)
}

// confirmAdmin checks the admin password sent with a destructive request
// (deleting a share or a folder): a second confirmation by whoever sits at
// the browser, throttled like the login. Writes the error and returns false
// when it doesn't match.
func (s *Server) confirmAdmin(w http.ResponseWriter, r *http.Request, password string) bool {
	ip := clientIP(r)
	if s.Throttle != nil && s.Throttle.Blocked(ip) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again in a few minutes")
		return false
	}
	st := s.Store.Read()
	u := st.FindUser(adminUser)
	if u == nil || !auth.CheckPassword(u.PasswordHash, password) {
		if s.Throttle != nil {
			s.Throttle.Fail(ip)
		}
		log.Printf("wrong admin password to confirm %s %s from %s", r.Method, r.URL.Path, ip)
		writeError(w, http.StatusForbidden, "wrong admin password")
		return false
	}
	return true
}
