package hardware

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"readynas/internal/disks"
	"readynas/internal/store"
)

// Fan control through the motherboard's sensor chip. hwmon exposes each
// fan output as pwmN (0-255, the duty cycle) and pwmN_enable:
//
//	0 = no control (full speed)   1 = manual (we write pwmN)   2+ = the chip's own automatic mode
//
// In "curve" mode nasd switches the output to manual and sets the speed
// from the hottest disk: disks are what the fan is there to protect, and
// they suffer above ~45 °C. Safety rules:
//   - no temperature available → full speed,
//   - any sensor at its critical limit → full speed,
//   - nasd stops (even a crash) → systemd runs `nasd -fans-full`
//     (ExecStopPost), so the fan never stays slow without a controller.

type Fan struct {
	Store *store.Store
	Disks *disks.Service
	Root  string // "/sys" (a fake tree in tests and in the VM)

	mu       sync.Mutex
	original map[string]string // pwm -> pwmN_enable before we took control
	status   FanStatus
}

type FanStatus struct {
	Mode        string    `json:"mode"`
	PWM         string    `json:"pwm"`
	Percent     int       `json:"percent"`     // what nasd set (curve/full)
	HottestDisk int       `json:"hottestDisk"` // °C, 0 if unknown
	Reason      string    `json:"reason"`
	Updated     time.Time `json:"updated"`
}

type PWMOutput struct {
	ID     string `json:"id"` // hwmon2/pwm1
	Chip   string `json:"chip"`
	Value  int    `json:"value"`  // 0-255
	Enable string `json:"enable"` // 0/1/2...
	FanRPM int    `json:"fanRpm"` // matching fanN_input, -1 if none
}

func NewFan(st *store.Store, d *disks.Service) *Fan {
	return &Fan{Store: st, Disks: d, Root: "/sys", original: map[string]string{}}
}

var pwmRe = regexp.MustCompile(`^pwm[0-9]+$`)

// Outputs lists the controllable fan outputs.
func (f *Fan) Outputs() []PWMOutput {
	chips, _ := filepath.Glob(filepath.Join(f.Root, "class", "hwmon", "hwmon*"))
	out := []PWMOutput{}
	for _, chip := range chips {
		files, _ := os.ReadDir(chip)
		for _, e := range files {
			if !pwmRe.MatchString(e.Name()) {
				continue
			}
			n := strings.TrimPrefix(e.Name(), "pwm")
			p := PWMOutput{ID: filepath.Base(chip) + "/" + e.Name(), Chip: read(chip, "name"), Enable: read(chip, e.Name()+"_enable"), FanRPM: -1}
			p.Value, _ = strconv.Atoi(read(chip, e.Name()))
			if rpm, err := strconv.Atoi(read(chip, "fan"+n+"_input")); err == nil {
				p.FanRPM = rpm
			}
			out = append(out, p)
		}
	}
	return out
}

func Validate(c store.FanSettings, outputs []PWMOutput) error {
	switch c.Mode {
	case "":
		return nil
	case "curve", "full":
	default:
		return errors.New("mode must be empty, curve or full")
	}
	if !slices.ContainsFunc(outputs, func(p PWMOutput) bool { return p.ID == c.PWM }) {
		return fmt.Errorf("unknown fan output %q", c.PWM)
	}
	if c.Mode == "curve" && (c.MinPercent < 20 || c.MinPercent > 100 || c.LowTemp < 20 || c.HighTemp > 70 || c.LowTemp >= c.HighTemp) {
		return errors.New("curve: minimum 20-100%, 20 °C ≤ low < high ≤ 70 °C")
	}
	return nil
}

func (f *Fan) Save(ctx context.Context, c store.FanSettings) error {
	if err := Validate(c, f.Outputs()); err != nil {
		return err
	}
	prev := f.Store.Read().Fan
	if err := f.Store.Update(func(st *store.State) error { st.Fan = c; return nil }); err != nil {
		return err
	}
	if prev.PWM != "" && (prev.PWM != c.PWM || c.Mode == "") {
		f.release(prev.PWM) // hand the old output back to the motherboard
	}
	f.Tick(ctx)
	return nil
}

// Run adjusts the fan every 15 seconds.
func (f *Fan) Run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		f.Tick(ctx)
		select {
		case <-ctx.Done():
			f.ReleaseAll()
			return
		case <-t.C:
		}
	}
}

// Percent computes the fan speed for the hottest disk temperature.
func Percent(c store.FanSettings, hottest int) int {
	switch {
	case c.Mode == "full" || hottest <= 0:
		return 100 // unknown temperature: be safe
	case hottest <= c.LowTemp:
		return c.MinPercent
	case hottest >= c.HighTemp:
		return 100
	}
	return c.MinPercent + (100-c.MinPercent)*(hottest-c.LowTemp)/(c.HighTemp-c.LowTemp)
}

func (f *Fan) Tick(ctx context.Context) {
	c := f.Store.Read().Fan
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = FanStatus{Mode: c.Mode, PWM: c.PWM, Updated: time.Now()}
	if c.Mode == "" || c.PWM == "" {
		f.status.Reason = "the motherboard controls the fan"
		return
	}
	hottest := 0
	if list, err := f.Disks.List(ctx); err == nil {
		for _, d := range list {
			if h := d.Health; h != nil && d.Role != "usb" && h.TemperatureC > hottest {
				hottest = h.TemperatureC
			}
		}
	}
	pct := Percent(c, hottest)
	f.status.HottestDisk = hottest
	switch {
	case c.Mode == "full":
		f.status.Reason = "always full speed"
	case hottest <= 0:
		f.status.Reason = "no disk temperature available: full speed for safety"
	default:
		f.status.Reason = fmt.Sprintf("hottest disk at %d °C", hottest)
	}
	if critical(f.Root) {
		pct, f.status.Reason = 100, "a sensor reached its critical temperature: full speed"
	}
	f.status.Percent = pct
	if err := f.set(c.PWM, pct); err != nil {
		f.status.Reason = "cannot control the fan: " + err.Error()
		log.Printf("fan: %v", err)
	}
}

func (f *Fan) Status() FanStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *Fan) pwmPath(id string) (string, error) {
	chip, pwm, ok := strings.Cut(id, "/")
	if !ok || !strings.HasPrefix(chip, "hwmon") || !pwmRe.MatchString(pwm) {
		return "", fmt.Errorf("invalid fan output %q", id)
	}
	return filepath.Join(f.Root, "class", "hwmon", chip, pwm), nil
}

func (f *Fan) set(id string, pct int) error {
	p, err := f.pwmPath(id)
	if err != nil {
		return err
	}
	if _, saved := f.original[id]; !saved {
		f.original[id] = strings.TrimSpace(readFile(p + "_enable"))
	}
	if strings.TrimSpace(readFile(p+"_enable")) != "1" {
		if err := os.WriteFile(p+"_enable", []byte("1"), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(p, []byte(strconv.Itoa(pct*255/100)), 0o644)
}

// release gives an output back to the motherboard's own control.
func (f *Fan) release(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, err := f.pwmPath(id)
	if err != nil {
		return
	}
	orig, ok := f.original[id]
	if !ok || orig == "" || orig == "1" {
		orig = "2" // automatic, when we don't know better
	}
	if os.WriteFile(p+"_enable", []byte(orig), 0o644) != nil {
		os.WriteFile(p, []byte("255"), 0o644) // can't hand it back: full speed
	}
	delete(f.original, id)
}

// ReleaseAll is called when nasd stops normally.
func (f *Fan) ReleaseAll() {
	f.mu.Lock()
	ids := make([]string, 0, len(f.original))
	for id := range f.original {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.release(id)
	}
}

// AllFansFull is the emergency brake run by systemd after nasd stops
// (`nasd -fans-full`): any output left in manual mode goes to full speed.
func AllFansFull(root string) {
	outs, _ := filepath.Glob(filepath.Join(root, "class", "hwmon", "hwmon*", "pwm*_enable"))
	for _, en := range outs {
		if strings.TrimSpace(readFile(en)) == "1" {
			os.WriteFile(strings.TrimSuffix(en, "_enable"), []byte("255"), 0o644)
		}
	}
}

func critical(root string) bool {
	inputs, _ := filepath.Glob(filepath.Join(root, "class", "hwmon", "hwmon*", "temp*_input"))
	for _, in := range inputs {
		v, err1 := strconv.Atoi(strings.TrimSpace(readFile(in)))
		c, err2 := strconv.Atoi(strings.TrimSpace(readFile(strings.TrimSuffix(in, "_input") + "_crit")))
		if err1 == nil && err2 == nil && c > 0 && v >= c {
			return true
		}
	}
	return false
}

func read(dir, file string) string { return strings.TrimSpace(readFile(filepath.Join(dir, file))) }

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}
