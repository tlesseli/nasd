package hardware

import (
	"context"
	"sync"
	"testing"
	"time"
)

// A simulated button: a list of (duration, pressed) steps.
func runPin(t *testing.T, hold time.Duration, steps []struct {
	d       time.Duration
	pressed bool
}) (short, long int) {
	t.Helper()
	var mu sync.Mutex
	state := false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		PinWatch{
			Read:    func() bool { mu.Lock(); defer mu.Unlock(); return state },
			Hold:    hold,
			OnShort: func() { mu.Lock(); short++; mu.Unlock() },
			OnLong:  func() { mu.Lock(); long++; mu.Unlock() },
		}.Run(ctx)
		close(done)
	}()
	for _, s := range steps {
		mu.Lock()
		state = s.pressed
		mu.Unlock()
		time.Sleep(s.d)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	return short, long
}

type step = struct {
	d       time.Duration
	pressed bool
}

func TestPinWatchShortPressWithBounce(t *testing.T) {
	ms := time.Millisecond
	short, long := runPin(t, time.Second, []step{
		{200 * ms, false},
		// bounce shorter than a poll: ignored, then a real press
		{10 * ms, true}, {10 * ms, false}, {10 * ms, true},
		{300 * ms, true},
		{300 * ms, false},
	})
	if short != 1 || long != 0 {
		t.Fatalf("short=%d long=%d, want 1 0", short, long)
	}
}

func TestPinWatchLongHoldFiresOnce(t *testing.T) {
	short, long := runPin(t, 500*time.Millisecond, []step{
		{200 * time.Millisecond, false},
		{1200 * time.Millisecond, true},
		{300 * time.Millisecond, false},
	})
	if short != 0 || long != 1 {
		t.Fatalf("short=%d long=%d, want 0 1", short, long)
	}
}

func TestPinWatchStuckPressedNeverActs(t *testing.T) {
	short, long := runPin(t, 300*time.Millisecond, []step{{1200 * time.Millisecond, true}})
	if short != 0 || long != 0 {
		t.Fatalf("short=%d long=%d, want 0 0", short, long)
	}
}
