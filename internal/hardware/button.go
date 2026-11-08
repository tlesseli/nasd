// Package hardware drives the NAS's own hardware: the front Backup button,
// the disk LEDs (to locate a disk) and the fan.
package hardware

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"readynas/internal/backups"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

// Buttons and keys reach Linux as input devices: /dev/input/eventN. Each
// read returns a 24-byte struct input_event (on 64-bit):
//
//	struct timeval time;  // 16 bytes
//	__u16 type;           // EV_KEY = 1 for buttons and keys
//	__u16 code;           // which key (KEY_F12 = 88, KEY_COPY = 133...)
//	__s32 value;          // 1 = pressed, 0 = released, 2 = auto-repeat
//
// Which device and code the ReadyNAS's Backup button produces isn't
// documented, so the UI learns it: "press the button now".

const (
	evKey     = 1
	eventSize = 24
)

type KeyPress struct {
	Device string `json:"device"` // device name, e.g. "gpio-keys"
	Code   int    `json:"code"`
}

type Button struct {
	Store   *store.Store
	Backups *backups.Service
	Jobs    *jobs.Manager
	Notify  func(kind, subject, body string)
	Input   string // /dev/input
	SysIn   string // /sys/class/input

	// OnPress is called when the configured button is pressed (wakes the display).
	OnPress func()

	mu       sync.Mutex
	cur      int // job being run (1-based) while running
	total    int
	open     map[string]bool // event devices being read
	learners []chan KeyPress
	running  bool
	lastPush time.Time
}

func NewButton(st *store.Store, b *backups.Service, j *jobs.Manager) *Button {
	return &Button{Store: st, Backups: b, Jobs: j, Input: "/dev/input", SysIn: "/sys/class/input",
		Notify: func(string, string, string) {}, open: map[string]bool{}}
}

// Run reads all input devices, rescanning every 30 s for new ones.
func (b *Button) Run(ctx context.Context) {
	for {
		paths, _ := filepath.Glob(filepath.Join(b.Input, "event*"))
		for _, p := range paths {
			b.mu.Lock()
			known := b.open[p]
			b.open[p] = true
			b.mu.Unlock()
			if !known {
				go b.read(ctx, p)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
	}
}

func (b *Button) deviceName(eventPath string) string {
	data, _ := os.ReadFile(filepath.Join(b.SysIn, filepath.Base(eventPath), "device", "name"))
	return strings.TrimSpace(string(data))
}

func (b *Button) read(ctx context.Context, path string) {
	defer func() {
		b.mu.Lock()
		delete(b.open, path)
		b.mu.Unlock()
	}()
	f, err := os.Open(path)
	if err != nil {
		return
	}
	go func() { <-ctx.Done(); f.Close() }()
	name := b.deviceName(path)
	buf := make([]byte, eventSize)
	for {
		if _, err := io.ReadFull(f, buf); err != nil {
			return // device gone (unplugged) or nasd stopping
		}
		typ := binary.LittleEndian.Uint16(buf[16:18])
		code := binary.LittleEndian.Uint16(buf[18:20])
		value := int32(binary.LittleEndian.Uint32(buf[20:24]))
		if typ == evKey && value == 1 {
			b.HandlePress(KeyPress{Device: name, Code: int(code)})
		}
	}
}

// HandlePress is called for every key press on any input device.
func (b *Button) HandlePress(k KeyPress) {
	b.mu.Lock()
	if len(b.learners) > 0 { // learn mode: the press is the answer, not a trigger
		for _, ch := range b.learners {
			ch <- k
		}
		b.learners = nil
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	cfg := b.Store.Read().Button
	if cfg.Device != "" && k.Device == cfg.Device && k.Code == cfg.Code {
		if b.OnPress != nil {
			b.OnPress()
		}
		b.Press("button")
	}
}

// Learn waits for the next key press on any input device.
func (b *Button) Learn(ctx context.Context) (KeyPress, error) {
	ch := make(chan KeyPress, 1)
	b.mu.Lock()
	b.learners = append(b.learners, ch)
	b.mu.Unlock()
	select {
	case k := <-ch:
		return k, nil
	case <-ctx.Done():
		b.mu.Lock()
		b.learners = nil
		b.mu.Unlock()
		return KeyPress{}, errors.New("no button press detected")
	}
}

// Press runs the configured backup jobs one after the other.
func (b *Button) Press(trigger string) error {
	cfg := b.Store.Read().Button
	b.mu.Lock()
	defer b.mu.Unlock()
	// A press while jobs are running, or a bounce of the contact, is ignored.
	if b.running || time.Since(b.lastPush) < 3*time.Second {
		return errors.New("the button's jobs are already running")
	}
	if len(cfg.Jobs) == 0 {
		return errors.New("no backup job is assigned to the button")
	}
	b.running, b.lastPush = true, time.Now()
	log.Printf("backup button pressed (%s): %d jobs", trigger, len(cfg.Jobs))
	go func() {
		results := b.runAll(cfg.Jobs)
		b.Notify("button", "Backup button: jobs finished", strings.Join(results, "\n"))
		b.mu.Lock()
		b.running = false // only once everything, notification included, is done
		b.mu.Unlock()
	}()
	return nil
}

func (b *Button) runAll(ids []int) []string {
	var results []string
	for i, id := range ids {
		b.mu.Lock()
		b.cur, b.total = i+1, len(ids)
		b.mu.Unlock()
		var snap jobs.Snapshot
		var err error
		// Wait for the NAS to be free (another job may be running), up to an hour.
		for start := time.Now(); time.Since(start) < time.Hour; time.Sleep(2 * time.Second) {
			if snap, err = b.Backups.Start(id, "button"); !errors.Is(err, jobs.ErrBusy) {
				break
			}
		}
		if err != nil {
			results = append(results, fmt.Sprintf("job %d: not started: %v", id, err))
			continue
		}
		for {
			s, ok := b.Jobs.Get(snap.ID)
			if !ok || s.Status != jobs.Running {
				results = append(results, fmt.Sprintf("%s: %s %s", s.Title, s.Status, s.Error))
				break
			}
			time.Sleep(time.Second)
		}
	}
	return results
}

// Progress returns (running, current job number, number of jobs).
func (b *Button) Progress() (bool, int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running, b.cur, b.total
}

// Running reports whether the button's jobs are in progress.
func (b *Button) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}
