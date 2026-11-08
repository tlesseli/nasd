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
	"io"
	"os"
	"os/exec"
	"strings"
)

type Func func(ctx context.Context, name string, args ...string) ([]byte, error)

// Exec runs a real command. Arguments are passed directly to the program,
// never through a shell, so a volume name like "x; rm -rf /" can't inject
// commands (and we validate names anyway).
func Exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return execute(ctx, nil, nil, name, args...)
}

// InputFunc is a Func that also writes data to the command's stdin.
type InputFunc func(ctx context.Context, stdin string, name string, args ...string) ([]byte, error)

// ExecInput is Exec with stdin. Secrets (passwords) go through stdin, never
// through arguments: every user on the machine can read arguments with `ps`.
func ExecInput(ctx context.Context, stdin string, name string, args ...string) ([]byte, error) {
	return execute(ctx, strings.NewReader(stdin), nil, name, args...)
}

// EnvFunc is a Func with extra environment variables ("KEY=value").
type EnvFunc func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

// ExecEnv runs a command with extra environment variables. Like stdin, the
// environment keeps secrets out of the arguments: /proc/<pid>/environ is
// readable only by the process's owner and root.
func ExecEnv(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	return execute(ctx, nil, env, name, args...)
}

func execute(ctx context.Context, stdin io.Reader, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(string(out))
		}
		return out, fmt.Errorf("%s failed (%w): %s", name, err, msg) // %w: callers can inspect the exit code
	}
	// Tools started with an environment (rclone) report their statistics on
	// stderr: keep them with the output so the job log and summary see them.
	if env != nil && stderr.Len() > 0 {
		out = append(out, stderr.Bytes()...)
	}
	return out, nil
}
