package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// Sessions maps a random token (stored in the browser cookie) to a user.
// They live in memory: restarting nasd logs everyone out, which is fine
// for a home NAS and means there's no session file to protect.
type Sessions struct {
	mu  sync.Mutex
	m   map[string]session
	ttl time.Duration
}

type session struct {
	user    string
	expires time.Time
}

func NewSessions(ttl time.Duration) *Sessions {
	return &Sessions{m: map[string]session{}, ttl: ttl}
}

func (s *Sessions) Create(user string) string {
	b := make([]byte, 32)
	rand.Read(b) // since Go 1.24, crypto/rand.Read never returns an error
	token := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gc()
	s.m[token] = session{user: user, expires: time.Now().Add(s.ttl)}
	return token
}

// Get returns the session's user and extends it (sliding expiry: you stay
// logged in as long as you keep using the UI).
func (s *Sessions) Get(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok || time.Now().After(sess.expires) {
		delete(s.m, token)
		return "", false
	}
	sess.expires = time.Now().Add(s.ttl)
	s.m[token] = sess
	return sess.user, true
}

func (s *Sessions) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, token)
}

// DeleteUser logs a user out everywhere (e.g. after a password change).
func (s *Sessions) DeleteUser(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for t, sess := range s.m {
		if sess.user == user {
			delete(s.m, t)
		}
	}
}

func (s *Sessions) gc() {
	now := time.Now()
	for t, sess := range s.m {
		if now.After(sess.expires) {
			delete(s.m, t)
		}
	}
}

// Throttle blocks an IP after too many failed logins in a time window.
type Throttle struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func NewThrottle(max int, window time.Duration) *Throttle {
	return &Throttle{max: max, window: window, fails: map[string][]time.Time{}}
}

func (t *Throttle) Blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.recent(ip)) >= t.max
}

func (t *Throttle) Fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.fails[ip] = append(t.recent(ip), time.Now())
}

func (t *Throttle) Reset(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.fails, ip)
}

// ResetAll forgets every failed attempt (the admin password was just reset
// at the NAS: whoever did it must be able to sign in right away).
func (t *Throttle) ResetAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	clear(t.fails)
}

func (t *Throttle) recent(ip string) []time.Time {
	cutoff := time.Now().Add(-t.window)
	var out []time.Time
	for _, f := range t.fails[ip] {
		if f.After(cutoff) {
			out = append(out, f)
		}
	}
	return out
}
