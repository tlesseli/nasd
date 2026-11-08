// Package run executes system commands. Everything nasd does to the system
// goes through a run.Func, which gives us two things:
//   - errors carry the tool's own message (stderr), e.g. "mdadm: /dev/sda is busy",
//     which is what you want to show in the UI;
//   - tests can swap in a fake Func that records commands instead of running them.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Func func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs a real command. Arguments are passed directly to the program,
// never through a shell, so a volume name like "x; rm -rf /" can't inject
// commands (and we validate names anyway).
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return out, fmt.Errorf("%s failed (%v): %s", name, err, msg)
	}
	return out, nil
}
