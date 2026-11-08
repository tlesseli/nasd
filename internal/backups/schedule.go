package backups

import (
	"context"
	"errors"
	"log"
	"time"

	"readynas/internal/jobs"
	"readynas/internal/store"
)

// The scheduler lives inside nasd rather than in systemd timers: scheduled
// runs then go through the same job queue as manual ones (one disk operation
// at a time, same log in the UI). If the NAS is busy when a backup is due,
// it waits and starts as soon as possible, up to an hour late.
//
// Runs missed while the NAS was off are not caught up: the next scheduled
// run copies everything that changed anyway.

const maxDelay = time.Hour

// due reports whether a schedule fires at minute t.
func due(s store.Schedule, t time.Time) bool {
	if t.Minute() != s.Minute {
		return false
	}
	switch s.Kind {
	case "hourly":
		return true
	case "daily":
		return t.Hour() == s.Hour
	case "weekly":
		return t.Hour() == s.Hour && int(t.Weekday()) == s.Weekday
	}
	return false
}

// RunScheduler checks schedules every 20 seconds until ctx is cancelled.
func (s *Service) RunScheduler(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick()
		}
	}
}

func (s *Service) tick() {
	now := s.Now().Truncate(time.Minute)
	st := s.Store.Read()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range st.Backups {
		if due(b.Schedule, now) && !s.lastFired[b.ID].Equal(now) {
			s.lastFired[b.ID] = now
			s.pending[b.ID] = now
		}
	}
	for id, since := range s.pending {
		if st.FindBackup(id) == nil { // deleted meanwhile
			delete(s.pending, id)
			continue
		}
		_, err := s.Start(id, "scheduled")
		switch {
		case err == nil:
			delete(s.pending, id)
		case errors.Is(err, jobs.ErrBusy) && now.Sub(since) < maxDelay:
			// try again at the next tick
		default:
			delete(s.pending, id)
			log.Printf("scheduled backup %d not started: %v", id, err)
			s.Store.Update(func(st *store.State) error {
				if b := st.FindBackup(id); b != nil {
					b.LastRun = store.BackupRun{Status: "failed", Started: now, Ended: s.Now(),
						Summary: "not started: " + err.Error()}
				}
				return nil
			})
		}
	}
}
