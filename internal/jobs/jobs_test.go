package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOneJobAtATime(t *testing.T) {
	release := make(chan struct{})
	m := NewManager(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return []byte("line1\nline2"), nil
	})
	first, err := m.Start("first", time.Minute, func(ctx context.Context, j *Job) error {
		<-release
		_, err := j.Run(ctx, "echo", "hi")
		return err
	})
	if err != nil || first.Status != Running {
		t.Fatalf("start: %v %+v", err, first)
	}
	if _, err := m.Start("second", time.Minute, func(context.Context, *Job) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("second job must be refused while the first runs, got %v", err)
	}
	close(release)
	m.Wait()

	got, _ := m.Get(first.ID)
	if got.Status != Done || !strings.Contains(strings.Join(got.Log, "\n"), "$ echo hi\n  line1\n  line2") {
		t.Fatalf("job: %+v", got)
	}

	failed, err := m.Start("failing", time.Minute, func(context.Context, *Job) error { return errors.New("boom") })
	if err != nil {
		t.Fatalf("a new job must be accepted once the first is done: %v", err)
	}
	m.Wait()
	if got, _ := m.Get(failed.ID); got.Status != Failed || got.Error != "boom" {
		t.Fatalf("failed job: %+v", got)
	}
	if list := m.List(); len(list) != 2 || list[0].ID != failed.ID {
		t.Fatalf("list should be newest first: %+v", list)
	}
}
