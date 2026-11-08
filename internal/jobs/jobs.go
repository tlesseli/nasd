// Package jobs runs long disk operations (creating a RAID, erasing a disk...)
// in the background, one at a time, with a log the UI can display.
//
// Why one at a time? Two operations touching disks concurrently (say, creating
// a volume while erasing one of its disks) is how you lose data. A single
// global "busy" flag is crude but impossible to get wrong.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"readynas/internal/run"
)

var ErrBusy = errors.New("another disk operation is already running, wait for it to finish")

const (
	Running = "running"
	Done    = "done"
	Failed  = "failed"

	keepJobs       = 20
	maxOutputLines = 40 // per command, mkfs is chatty and RAM is scarce
)

// Snapshot is a copy of a job's state, safe to serialize while it runs.
type Snapshot struct {
	ID      int       `json:"id"`
	Title   string    `json:"title"`
	Status  string    `json:"status"`
	Log     []string  `json:"log"`
	Error   string    `json:"error,omitempty"`
	Started time.Time `json:"started"`
	Ended   time.Time `json:"ended,omitzero"`
}

type Job struct {
	mu   sync.Mutex
	snap Snapshot
	exec run.Func
}

func (j *Job) Logf(format string, args ...any) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.snap.Log = append(j.snap.Log, fmt.Sprintf(format, args...))
}

// Run executes a command and records it (and its output) in the job log.
// Its signature matches run.Func so job steps can pass j.Run around.
func (j *Job) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	j.Logf("$ %s %s", name, strings.Join(args, " "))
	out, err := j.exec(ctx, name, args...)
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > maxOutputLines {
		lines = append(lines[:maxOutputLines], fmt.Sprintf("... (%d more lines)", len(lines)-maxOutputLines))
	}
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			j.Logf("  %s", l)
		}
	}
	if err != nil {
		j.Logf("  ! %v", err)
	}
	return out, err
}

func (j *Job) snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := j.snap
	s.Log = append([]string(nil), j.snap.Log...)
	return s
}

type Manager struct {
	mu      sync.Mutex
	exec    run.Func
	nextID  int
	running bool
	jobs    []*Job // most recent last
	wg      sync.WaitGroup
}

func NewManager(exec run.Func) *Manager {
	return &Manager{exec: exec}
}

// Start launches fn in the background, or returns ErrBusy.
func (m *Manager) Start(title string, fn func(ctx context.Context, j *Job) error) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return Snapshot{}, ErrBusy
	}
	m.running = true
	m.nextID++
	j := &Job{exec: m.exec, snap: Snapshot{ID: m.nextID, Title: title, Status: Running, Started: time.Now()}}
	m.jobs = append(m.jobs, j)
	if len(m.jobs) > keepJobs {
		m.jobs = m.jobs[len(m.jobs)-keepJobs:]
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// Not the HTTP request's context: the job must survive the browser
		// closing. The timeout is a safety net against a hung command.
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		err := fn(ctx, j)

		j.mu.Lock()
		j.snap.Ended = time.Now()
		if err != nil {
			j.snap.Status, j.snap.Error = Failed, err.Error()
		} else {
			j.snap.Status = Done
		}
		j.mu.Unlock()

		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()
	return j.snapshot(), nil
}

func (m *Manager) Get(id int) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.snap.ID == id {
			return j.snapshot(), true
		}
	}
	return Snapshot{}, false
}

// List returns recent jobs, newest first.
func (m *Manager) List() []Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Snapshot, 0, len(m.jobs))
	for i := len(m.jobs) - 1; i >= 0; i-- {
		out = append(out, m.jobs[i].snapshot())
	}
	return out
}

// Wait blocks until all started jobs have finished (used by tests).
func (m *Manager) Wait() { m.wg.Wait() }
