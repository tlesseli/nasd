// Package health reads hardware sensors, full SMART reports and system logs.
//
// Sensors come from /sys/class/hwmon: every temperature or fan chip the
// kernel knows exposes files like temp1_input (millidegrees) or fan1_input
// (RPM). On the ReadyNAS, the board's sensor chip (it87 / w83627 family)
// reports the fan and system temperatures; the CPU (coretemp) its own.
package health

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"readynas/internal/run"
)

type Sensor struct {
	ID    string  `json:"id"`   // hwmon2/temp1: stable while the system runs
	Chip  string  `json:"chip"` // coretemp, it87...
	Label string  `json:"label"`
	Kind  string  `json:"kind"`  // "temp" (°C) or "fan" (RPM)
	Value float64 `json:"value"` //
	Limit float64 `json:"limit"` // temp: critical/max °C; fan: minimum RPM; 0 if unknown
}

// Sensors lists temperatures and fans. sys is "/sys" (a temp dir in tests).
func Sensors(sys string) []Sensor {
	chips, _ := filepath.Glob(filepath.Join(sys, "class", "hwmon", "hwmon*"))
	out := []Sensor{}
	for _, chip := range chips {
		name := readStr(chip, "name")
		inputs, _ := filepath.Glob(filepath.Join(chip, "*_input"))
		for _, in := range inputs {
			base := strings.TrimSuffix(filepath.Base(in), "_input") // temp1, fan2...
			var kind string
			var scale float64
			switch {
			case strings.HasPrefix(base, "temp"):
				kind, scale = "temp", 1000 // millidegrees
			case strings.HasPrefix(base, "fan"):
				kind, scale = "fan", 1
			default:
				continue // voltages, power... not shown
			}
			v, err := strconv.ParseFloat(readStr(chip, base+"_input"), 64)
			if err != nil || (kind == "temp" && v <= -100000) {
				continue // -128 °C: nothing wired to that sensor input (IT8721F temp3)
			}
			s := Sensor{ID: filepath.Base(chip) + "/" + base, Chip: name, Kind: kind, Value: v / scale,
				Label: readStr(chip, base+"_label")}
			if s.Label == "" {
				s.Label = base
			}
			for _, lim := range []string{"_crit", "_max", "_min"} {
				if l, err := strconv.ParseFloat(readStr(chip, base+lim), 64); err == nil && l > 0 {
					if (kind == "temp") == (lim != "_min") {
						s.Limit = l / scale
						break
					}
				}
			}
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func readStr(dir, file string) string {
	b, _ := os.ReadFile(filepath.Join(dir, file))
	return strings.TrimSpace(string(b))
}

var diskRe = regexp.MustCompile(`^(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+)$`)

// SmartReport returns smartctl's full text report for a disk ("SMART+" in
// the original ReadyNAS UI): attributes, error log, self-test log.
func SmartReport(ctx context.Context, r run.Func, disk string) (string, error) {
	if !diskRe.MatchString(disk) {
		return "", errors.New("invalid disk name")
	}
	// smartctl's exit code is a bit mask with many benign bits: keep the text.
	out, err := r(ctx, "smartctl", "-x", "/dev/"+disk)
	if len(out) == 0 && err != nil {
		return "", err
	}
	return string(out), nil
}

// Log sources the UI can show, mapped to journalctl arguments.
var logSources = map[string][]string{
	"all":    {},
	"nasd":   {"-u", "nasd"},
	"kernel": {"-k"},
	"samba":  {"-u", "smbd", "-u", "nmbd"},
	"rsync":  {"-u", "rsync"},
	"raid":   {"-u", "mdmonitor"},
}

func LogSources() []string {
	keys := make([]string, 0, len(logSources))
	for k := range logSources {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Logs returns the last lines of the system journal, newest last.
func Logs(ctx context.Context, r run.Func, source string, lines int, errorsOnly bool) ([]string, error) {
	extra, ok := logSources[source]
	if !ok {
		return nil, fmt.Errorf("unknown log source %q", source)
	}
	lines = min(max(lines, 10), 2000)
	args := append([]string{"--no-pager", "-o", "short-iso", "-n", strconv.Itoa(lines)}, extra...)
	if errorsOnly {
		args = append(args, "-p", "warning")
	}
	out, err := r(ctx, "journalctl", args...)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n"), nil
}
