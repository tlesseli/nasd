package hardware

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Locate a disk: make its bay light blink so you pull the right one.
//
// Most bays have an activity LED wired to the disk itself: it lights when
// the disk works. Reading the disk in bursts (0.5 s on, 0.5 s off) makes
// it blink, whatever the hardware. The reads use O_DIRECT: through the
// page cache, repeated reads would come from RAM and the disk (and its LED)
// would stay idle. If the enclosure supports the standard locate LED
// (ledmon's ledctl), it is switched on as well.

type Locator struct {
	Dev string // /dev
	// LED, when set, blinks the bay's own LED (on the ReadyNAS the bay LEDs
	// are chipset pins, not wired to disk activity). It returns false when
	// the disk has no LED (then the reading method is used).
	LED    func(disk string, blink bool) bool
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func NewLocator() *Locator {
	return &Locator{Dev: "/dev", active: map[string]context.CancelFunc{}}
}

var locateDiskRe = regexp.MustCompile(`^(sd[a-z]+|nvme[0-9]+n[0-9]+)$`)

// Start blinks a disk for the given duration (or until Stop).
func (l *Locator) Start(disk string, d time.Duration) error {
	if !locateDiskRe.MatchString(disk) {
		return errors.New("invalid disk name")
	}
	if l.LED != nil {
		l.Stop(disk)
		if l.LED(disk, true) {
			ctx, cancel := context.WithTimeout(context.Background(), d)
			l.mu.Lock()
			l.active[disk] = cancel
			l.mu.Unlock()
			go func() {
				<-ctx.Done()
				l.LED(disk, false)
				l.mu.Lock()
				if ctx.Err() == context.DeadlineExceeded {
					delete(l.active, disk)
				}
				l.mu.Unlock()
			}()
			return nil
		}
	}
	path := l.Dev + "/" + disk
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECT, 0)
	if err != nil {
		return err
	}
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil || size < 1<<20 {
		f.Close()
		return errors.New("cannot read the disk size")
	}
	l.Stop(disk)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	l.mu.Lock()
	l.active[disk] = cancel
	l.mu.Unlock()

	if _, err := exec.LookPath("ledctl"); err == nil {
		exec.Command("ledctl", "locate="+path).Run() // best effort
	}
	go func() {
		defer f.Close()
		defer func() {
			if _, err := exec.LookPath("ledctl"); err == nil {
				exec.Command("ledctl", "locate_off="+path).Run()
			}
			l.mu.Lock()
			if _, ok := l.active[disk]; ok && ctx.Err() == context.DeadlineExceeded {
				delete(l.active, disk)
			}
			l.mu.Unlock()
		}()
		blink(ctx, f, size)
	}()
	return nil
}

func blink(ctx context.Context, f *os.File, size int64) {
	// O_DIRECT needs a memory buffer aligned on the disk's block size: an
	// anonymous mmap is aligned on a page (4096 bytes), which is enough.
	const chunk = 1 << 20
	buf, err := syscall.Mmap(-1, 0, chunk, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		return
	}
	defer syscall.Munmap(buf)
	blocks := size / chunk
	for ctx.Err() == nil {
		on := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(on) && ctx.Err() == nil {
			// Random places: sequential reads might be served by the disk's
			// own read-ahead cache without much activity.
			f.ReadAt(buf, rand.Int64N(blocks)*chunk)
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (l *Locator) Stop(disk string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if cancel, ok := l.active[disk]; ok {
		cancel()
		delete(l.active, disk)
	}
}

// Active lists the disks currently blinking.
func (l *Locator) Active() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []string{}
	for d := range l.active {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}
