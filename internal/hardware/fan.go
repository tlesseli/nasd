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
// nasd never uses the chip's automatic mode: on the ReadyNAS it lets the fan
// slow down until it stalls (it stopped at 30%). Every output with a fan is
// in manual mode: the controlled one follows the chosen mode, any other one
// runs at full speed, and so do all of them when nasd stops.
//
// The fan test (Calibrate) runs once per output, at the first start and on
// demand: at 100% a fan must turn (otherwise the output has none and can't be
// chosen), then the speed goes down 10% at a time until it stalls, and the
// lowest speed it runs at is checked to also restart it from standstill.
// That minimum (+10% margin) is a floor nasd never goes below.
//
// Safety rules:
//   - no temperature available → full speed,
//   - any sensor at its critical limit → full speed,
//   - the controlled fan reads 0 RPM while it should turn → full speed, its
//     minimum goes up 10%, alert,
//   - nasd stops (even a crash) → systemd runs `nasd -fans-full`
//     (ExecStopPost), so the fan never stays slow without a controller.

type Fan struct {
	Store  *store.Store
	Disks  *disks.Service
	Root   string // "/sys" (a fake tree in tests and in the VM)
	Notify func(kind, subject, body string)
	// ReadRPM and Settle: how the fan test reads a speed, and how long it
	// waits for the fan to settle at each step (tests replace them).
	ReadRPM func(id string) int
	Settle  time.Duration

	mu      sync.Mutex
	touched map[string]bool // outputs nasd put in manual mode
	stalls  map[string]int  // consecutive ticks at 0 RPM while it should turn
	testing string          // output under test ("" = none): control paused
	status  FanStatus
}

type FanStatus struct {
	Mode        string    `json:"mode"`
	PWM         string    `json:"pwm"`
	Percent     int       `json:"percent"`     // what nasd set
	HottestDisk int       `json:"hottestDisk"` // °C, 0 if unknown
	Reason      string    `json:"reason"`
	Testing     string    `json:"testing"` // output under test, if any
	Updated     time.Time `json:"updated"`
}

// FixedSafetyTemp: in "fixed" mode, a disk this hot sends the fan to 100%.
const FixedSafetyTemp = 50

// floorPercent: never below this, whatever the test measured.
const floorPercent = 20

type PWMOutput struct {
	ID     string `json:"id"` // hwmon2/pwm1
	Chip   string `json:"chip"`
	Value  int    `json:"value"`  // 0-255
	Enable string `json:"enable"` // 0/1/2...
	FanRPM int    `json:"fanRpm"` // matching fanN_input, -1 if none
	MinRPM int    `json:"minRpm"` // the chip's low-speed alarm limit (fanN_min), 0 if none
	// Input: the output has a speed sensor (fanN_input). HasFan: a fan turns
	// on it (tested, or seen turning). Only those can be chosen.
	Input      bool `json:"input"`
	HasFan     bool `json:"hasFan"`
	Tested     bool `json:"tested"`
	MinPercent int  `json:"minPercent"` // lowest allowed speed (tested), else floorPercent
}

func NewFan(st *store.Store, d *disks.Service) *Fan {
	return &Fan{Store: st, Disks: d, Root: "/sys", Settle: 6 * time.Second,
		touched: map[string]bool{}, stalls: map[string]int{}, Notify: func(string, string, string) {}}
}

var pwmRe = regexp.MustCompile(`^pwm[0-9]+$`)

// Outputs lists the fan outputs, with what the test learned about them.
func (f *Fan) Outputs() []PWMOutput {
	calib := f.Store.Read().Fan.Calib
	chips, _ := filepath.Glob(filepath.Join(f.Root, "class", "hwmon", "hwmon*"))
	out := []PWMOutput{}
	for _, chip := range chips {
		files, _ := os.ReadDir(chip)
		for _, e := range files {
			if !pwmRe.MatchString(e.Name()) {
				continue
			}
			n := strings.TrimPrefix(e.Name(), "pwm")
			p := PWMOutput{ID: filepath.Base(chip) + "/" + e.Name(), Chip: read(chip, "name"), Enable: read(chip, e.Name()+"_enable"),
				FanRPM: -1, MinPercent: floorPercent}
			p.Value, _ = strconv.Atoi(read(chip, e.Name()))
			if rpm, err := strconv.Atoi(read(chip, "fan"+n+"_input")); err == nil {
				p.FanRPM, p.Input = rpm, true
			}
			p.MinRPM, _ = strconv.Atoi(read(chip, "fan"+n+"_min"))
			if c, ok := calib[p.ID]; ok {
				p.Tested, p.HasFan = true, c.HasFan
				p.MinPercent = max(c.MinPercent, floorPercent)
			} else {
				p.HasFan = p.FanRPM > 0
			}
			out = append(out, p)
		}
	}
	return out
}

func findOutput(outputs []PWMOutput, id string) (PWMOutput, bool) {
	i := slices.IndexFunc(outputs, func(p PWMOutput) bool { return p.ID == id })
	if i < 0 {
		return PWMOutput{}, false
	}
	return outputs[i], true
}

func Validate(c store.FanSettings, outputs []PWMOutput) error {
	switch c.Mode {
	case "", "full":
		if c.PWM == "" {
			return nil // every fan at full speed
		}
	case "curve", "fixed":
	default:
		return errors.New("mode must be curve, fixed or full")
	}
	o, ok := findOutput(outputs, c.PWM)
	switch {
	case !ok:
		return fmt.Errorf("unknown fan output %q", c.PWM)
	case !o.HasFan:
		return fmt.Errorf("no fan turns on %s: choose an output with a fan", c.PWM)
	}
	if c.Mode == "curve" && (c.MinPercent < o.MinPercent || c.MinPercent > 100 || c.LowTemp < 20 || c.HighTemp > 70 || c.LowTemp >= c.HighTemp) {
		return fmt.Errorf("curve: minimum %d-100%% (this fan stalls below %d%%), 20 °C ≤ low < high ≤ 70 °C", o.MinPercent, o.MinPercent)
	}
	if c.Mode == "fixed" && (c.FixedPercent < o.MinPercent || c.FixedPercent > 100) {
		return fmt.Errorf("fixed speed: %d-100%% (this fan stalls below %d%%)", o.MinPercent, o.MinPercent)
	}
	return nil
}

func (f *Fan) Save(ctx context.Context, c store.FanSettings) error {
	if err := Validate(c, f.Outputs()); err != nil {
		return err
	}
	prev := f.Store.Read().Fan
	c.MaxRPM, c.Calib = max(c.MaxRPM, prev.MaxRPM), prev.Calib // measured, not settings
	if err := f.Store.Update(func(st *store.State) error { st.Fan = c; return nil }); err != nil {
		return err
	}
	f.Tick(ctx) // the previous output, if any, now runs at full speed
	return nil
}

// Run tests the outputs not tested yet, then adjusts the fans every 15 s.
func (f *Fan) Run(ctx context.Context) {
	f.Tick(ctx) // every fan to a safe speed first
	for _, o := range f.Outputs() {
		if o.Input && !o.Tested {
			if err := f.Calibrate(ctx, o.ID, func(format string, args ...any) { log.Printf("fan test: "+format, args...) }); err != nil {
				log.Printf("fan test %s: %v", o.ID, err)
			}
		}
	}
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
	case c.Mode == "full" || c.Mode == "" || hottest <= 0:
		return 100 // unknown temperature: be safe
	case c.Mode == "fixed":
		if hottest >= FixedSafetyTemp {
			return 100
		}
		return c.FixedPercent
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
	if f.testing != "" {
		f.status.Testing, f.status.Reason, f.status.Updated = f.testing, "fan test in progress", time.Now()
		return
	}
	f.status = FanStatus{Mode: c.Mode, PWM: c.PWM, Updated: time.Now()}
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
	case c.Mode == "full" || c.Mode == "":
		f.status.Reason = "always full speed"
	case hottest <= 0:
		f.status.Reason = "no disk temperature available: full speed for safety"
	case c.Mode == "fixed" && hottest >= FixedSafetyTemp:
		f.status.Reason = fmt.Sprintf("a disk reached %d °C: full speed until it cools down", hottest)
	case c.Mode == "fixed":
		f.status.Reason = fmt.Sprintf("fixed speed · hottest disk at %d °C", hottest)
	default:
		f.status.Reason = fmt.Sprintf("hottest disk at %d °C", hottest)
	}
	if critical(f.Root) {
		pct, f.status.Reason = 100, "a sensor reached its critical temperature: full speed"
	}

	for _, o := range f.Outputs() {
		// Outputs proven to drive nothing are left alone. Everything else is
		// in manual mode: never the chip's automatic mode.
		if o.Tested && !o.HasFan || !o.Tested && !o.Input {
			continue
		}
		target := 100
		if o.ID == c.PWM && c.Mode != "" && c.Mode != "full" {
			target = max(pct, o.MinPercent)
			f.status.Percent = target
			if f.stalled(o, target) {
				target = 100
			}
		}
		if err := f.set(o.ID, target); err != nil {
			f.status.Reason = "cannot control the fan: " + err.Error()
			log.Printf("fan: %v", err)
		}
	}
	if c.Mode == "full" || c.Mode == "" {
		f.status.Percent = 100
	}
	f.learnMaxRPM(c)
}

// stalled: the controlled fan reads 0 RPM while it should turn, two ticks in
// a row (caller holds f.mu). Its minimum goes up 10% and an alert is sent.
func (f *Fan) stalled(o PWMOutput, target int) bool {
	// The controlled output was checked to have a fan when it was chosen: at
	// 0 RPM it has stalled (not "no fan", as an untested output would look).
	if !o.Input || o.FanRPM > 0 {
		f.stalls[o.ID] = 0
		return false
	}
	f.stalls[o.ID]++
	if f.stalls[o.ID] < 2 {
		return false
	}
	newMin := min(100, max(o.MinPercent, target)+10)
	f.status.Reason = fmt.Sprintf("the fan stopped at %d%%: full speed, minimum raised to %d%%", target, newMin)
	log.Printf("fan %s: stalled at %d%%, minimum now %d%%", o.ID, target, newMin)
	go f.Store.Update(func(st *store.State) error {
		if st.Fan.Calib == nil {
			st.Fan.Calib = map[string]store.FanCalib{}
		}
		cal := st.Fan.Calib[o.ID]
		cal.Tested, cal.HasFan, cal.MinPercent = time.Now(), true, newMin
		st.Fan.Calib[o.ID] = cal
		if st.Fan.MinPercent < newMin {
			st.Fan.MinPercent = newMin
		}
		if st.Fan.FixedPercent < newMin {
			st.Fan.FixedPercent = newMin
		}
		return nil
	})
	go f.Notify("fan_stall", "The fan stopped at "+strconv.Itoa(target)+"%",
		"The fan on "+o.ID+" stopped turning at "+strconv.Itoa(target)+"%. It is back at full speed, and its minimum speed is now "+strconv.Itoa(newMin)+"%.")
	f.stalls[o.ID] = 0
	return true
}

// Calibrate runs the fan test of one output (about a minute and a half).
func (f *Fan) Calibrate(ctx context.Context, id string, logf func(string, ...any)) error {
	if _, err := f.pwmPath(id); err != nil {
		return err
	}
	f.mu.Lock()
	if f.testing != "" {
		f.mu.Unlock()
		return fmt.Errorf("a fan test is already running (%s)", f.testing)
	}
	f.testing = id
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.testing = ""
		f.mu.Unlock()
		f.Tick(context.Background()) // back to the chosen mode
	}()
	rpm := f.ReadRPM
	if rpm == nil {
		rpm = f.sysfsRPM
	}
	wait := func() error {
		select {
		case <-ctx.Done():
			f.mu.Lock()
			f.set(id, 100)
			f.mu.Unlock()
			return ctx.Err()
		case <-time.After(f.Settle):
			return nil
		}
	}
	at := func(pct int) (int, error) {
		f.mu.Lock()
		err := f.set(id, pct)
		f.mu.Unlock()
		if err != nil {
			return 0, err
		}
		if err := wait(); err != nil {
			return 0, err
		}
		return rpm(id), nil
	}

	full, err := at(100)
	if err != nil {
		return err
	}
	cal := store.FanCalib{Tested: time.Now(), MaxRPM: full}
	if full == 0 {
		logf("%s: no fan turns at 100%%: this output drives nothing", id)
		return f.saveCalib(id, cal)
	}
	cal.HasFan = true
	logf("%s: 100%% → %d RPM", id, full)
	lowest := 100
	for pct := 90; pct >= 10; pct -= 10 {
		r, err := at(pct)
		if err != nil {
			return err
		}
		logf("%s: %d%% → %d RPM", id, pct, r)
		if r == 0 {
			cal.StallPercent = pct
			break
		}
		lowest = pct
	}
	// From standstill a fan needs more than to keep turning: start it at the
	// candidate minimum, raise it until it starts.
	candidate := max(floorPercent, lowest+10)
	if cal.StallPercent == 0 {
		candidate = max(floorPercent, lowest)
	}
	for ; candidate <= 100; candidate += 10 {
		if _, err := at(0); err != nil {
			return err
		}
		r, err := at(candidate)
		if err != nil {
			return err
		}
		logf("%s: restart from standstill at %d%% → %d RPM", id, candidate, r)
		if r > 0 {
			break
		}
	}
	cal.MinPercent = min(100, candidate)
	if _, err := at(100); err != nil {
		return err
	}
	logf("%s: fan OK, %d RPM at full speed; it stalls at %d%%: minimum set to %d%%", id, full, cal.StallPercent, cal.MinPercent)
	return f.saveCalib(id, cal)
}

func (f *Fan) saveCalib(id string, cal store.FanCalib) error {
	return f.Store.Update(func(st *store.State) error {
		if st.Fan.Calib == nil {
			st.Fan.Calib = map[string]store.FanCalib{}
		}
		st.Fan.Calib[id] = cal
		if cal.HasFan && st.Fan.PWM == id {
			st.Fan.MinPercent = max(st.Fan.MinPercent, cal.MinPercent)
			st.Fan.FixedPercent = max(st.Fan.FixedPercent, cal.MinPercent)
		}
		if cal.MaxRPM > st.Fan.MaxRPM {
			st.Fan.MaxRPM = cal.MaxRPM
		}
		return nil
	})
}

func (f *Fan) sysfsRPM(id string) int {
	p, err := f.pwmPath(id)
	if err != nil {
		return 0
	}
	n := strings.TrimPrefix(filepath.Base(p), "pwm")
	v, _ := strconv.Atoi(read(filepath.Dir(p), "fan"+n+"_input"))
	return v
}

// learnMaxRPM remembers the fastest speed seen: the fan's 100%, the top of
// the gauge. Saved only when it grows by more than 3% (rare writes).
func (f *Fan) learnMaxRPM(c store.FanSettings) {
	for _, o := range f.Outputs() {
		if o.FanRPM > c.MaxRPM*103/100 && o.HasFan {
			rpm := o.FanRPM
			go f.Store.Update(func(st *store.State) error { st.Fan.MaxRPM = max(st.Fan.MaxRPM, rpm); return nil })
		}
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

// set puts an output in manual mode at pct %.
func (f *Fan) set(id string, pct int) error {
	p, err := f.pwmPath(id)
	if err != nil {
		return err
	}
	f.touched[id] = true
	if strings.TrimSpace(readFile(p+"_enable")) != "1" {
		if err := os.WriteFile(p+"_enable", []byte("1"), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(p, []byte(strconv.Itoa(pct*255/100)), 0o644)
}

// ReleaseAll is called when nasd stops normally: every fan nasd controlled
// goes to full speed (never back to the chip's automatic mode).
func (f *Fan) ReleaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id := range f.touched {
		f.set(id, 100)
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
