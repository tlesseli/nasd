// Package store keeps nasd's own state (users, later shares and settings)
// in a single JSON file. A NAS has a handful of users and shares, so a JSON
// file is simpler than a database and you can read it with `cat`.
//
// Writes are atomic: we write a temp file, fsync it, then rename() it over
// the old one. rename() is atomic on Linux, so a power cut leaves either the
// old or the new file, never a half-written one.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type User struct {
	Name         string `json:"name"`
	PasswordHash string `json:"passwordHash"`
	Admin        bool   `json:"admin"`
}

// State is everything nasd persists. New fields get added here as the
// project grows (shares, alert settings, ...).
type State struct {
	Users []User `json:"users"`
}

func (s *State) FindUser(name string) *User {
	for i := range s.Users {
		if s.Users[i].Name == name {
			return &s.Users[i]
		}
	}
	return nil
}

type Store struct {
	mu    sync.Mutex
	path  string
	state State
}

func Open(path string) (*Store, error) {
	s := &Store{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil // first start: empty state
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.state); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return s, nil
}

// Read gives fn a copy of the state, so callers can't mutate it by accident.
func (s *Store) Read() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state)
}

// Update runs fn on a copy of the state and persists it. If fn returns an
// error (or saving fails), nothing changes: in memory or on disk.
func (s *Store) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(s.state)
	if err := fn(&next); err != nil {
		return err
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) save(st State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())              // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil { // contains password hashes
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func clone(st State) State {
	out := st
	out.Users = append([]User(nil), st.Users...)
	return out
}
