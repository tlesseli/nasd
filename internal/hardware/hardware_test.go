package hardware

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"readynas/internal/backups"
	"readynas/internal/disks"
	"readynas/internal/jobs"
	"readynas/internal/store"
)

func event(typ, code uint16, value int32) []byte {
	b := make([]byte, eventSize)
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

func newButton(t *testing.T) (*Button, *[]string) {
	dir := t.TempDir()
	st, _ := store.Open(filepath.Join(dir, "state.json"))
	fake := func(ctx context.Context, name string, args ...string) ([]byte, error) { return nil, nil }
	jm := jobs.NewManager(fake)
	b := NewButton(st, backups.NewService(st, jm, dir), jm)
	b.Input = filepath.Join(dir, "input")
	b.SysIn = filepath.Join(dir, "sys")
	var notes []string
	b.Notify = func(k, s, body string) { notes = append(notes, s+"\n"+body) }
	return b, &notes
}

func TestButtonLearnAndPress(t *testing.T) {
	b, notes := newButton(t)
	// A fake input device: "gpio-keys" sends a press + release of code 133.
	os.MkdirAll(b.Input, 0o755)
	os.MkdirAll(filepath.Join(b.SysIn, "event3", "device"), 0o755)
	os.WriteFile(filepath.Join(b.SysIn, "event3", "device", "name"), []byte("gpio-keys\n"), 0o644)
	stream := append(append(event(evKey, 133, 1), event(0, 0, 0)...), event(evKey, 133, 0)...)
	os.WriteFile(filepath.Join(b.Input, "event3"), stream, 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	learned := make(chan KeyPress)
	go func() { k, _ := b.Learn(ctx); learned <- k }()
	time.Sleep(50 * time.Millisecond)
	b.read(ctx, filepath.Join(b.Input, "event3")) // reads the file to EOF
	k := <-learned
	if k != (KeyPress{Device: "gpio-keys", Code: 133}) {
		t.Fatalf("learned %+v", k)
	}

	// Configured: a press runs the jobs (here a missing job, reported).
	b.Store.Update(func(st *store.State) error {
		st.Button = store.ButtonSettings{Device: "gpio-keys", Code: 133, Jobs: []int{42}}
		return nil
	})
	b.HandlePress(KeyPress{Device: "AT keyboard", Code: 133}) // other device: ignored
	b.HandlePress(KeyPress{Device: "gpio-keys", Code: 133})
	if err := b.Press("again"); err == nil {
		t.Error("a second press while running (or bouncing) must be ignored")
	}
	for i := 0; i < 100 && b.Running(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "job 42: not started") {
		t.Errorf("notes: %v", *notes)
	}
}

func fakeHwmon(t *testing.T, temp string) string {
	root := t.TempDir()
	chip := filepath.Join(root, "class", "hwmon", "hwmon2")
	os.MkdirAll(chip, 0o755)
	for f, v := range map[string]string{"name": "it87", "pwm1": "120", "pwm1_enable": "2", "fan1_input": "1500",
		"temp1_input": temp, "temp1_crit": "90000"} {
		os.WriteFile(filepath.Join(chip, f), []byte(v+"\n"), 0o644)
	}
	return root
}

func newFan(t *testing.T, root string, smart string) *Fan {
	st, _ := store.Open(filepath.Join(t.TempDir(), "state.json"))
	d := disks.NewService(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "lsblk" {
			return []byte(`{"blockdevices":[{"name":"sda","path":"/dev/sda","size":100,"type":"disk","tran":"sata","mountpoints":[null]}]}`), nil
		}
		return []byte(smart), nil
	})
	f := NewFan(st, d)
	f.Root = root
	return f
}

func TestFanCurve(t *testing.T) {
	c := store.FanSettings{Mode: "curve", MinPercent: 30, LowTemp: 35, HighTemp: 45}
	for temp, want := range map[int]int{0: 100, 30: 30, 35: 30, 40: 65, 45: 100, 50: 100} {
		if got := Percent(c, temp); got != want {
			t.Errorf("Percent(%d) = %d, want %d", temp, got, want)
		}
	}
}

func TestFanControlAndRelease(t *testing.T) {
	root := fakeHwmon(t, "40000")
	f := newFan(t, root, `{"smart_status":{"passed":true},"temperature":{"current":40}}`)
	chip := filepath.Join(root, "class", "hwmon", "hwmon2")
	if outs := f.Outputs(); len(outs) != 1 || outs[0].ID != "hwmon2/pwm1" || outs[0].FanRPM != 1500 || outs[0].Chip != "it87" {
		t.Fatalf("outputs: %+v", outs)
	}
	if err := f.Save(context.Background(), store.FanSettings{Mode: "curve", PWM: "hwmon2/pwm1", MinPercent: 30, LowTemp: 35, HighTemp: 45}); err != nil {
		t.Fatal(err)
	}
	if read(chip, "pwm1_enable") != "1" || read(chip, "pwm1") != "165" { // 65% of 255
		t.Errorf("enable=%s pwm=%s", read(chip, "pwm1_enable"), read(chip, "pwm1"))
	}
	if s := f.Status(); s.Percent != 65 || s.HottestDisk != 40 {
		t.Errorf("status %+v", s)
	}
	// Back to the motherboard: the original automatic mode returns.
	f.Save(context.Background(), store.FanSettings{})
	if read(chip, "pwm1_enable") != "2" {
		t.Errorf("not released: enable=%s", read(chip, "pwm1_enable"))
	}
	if err := f.Save(context.Background(), store.FanSettings{Mode: "curve", PWM: "hwmon9/pwm1", MinPercent: 30, LowTemp: 35, HighTemp: 45}); err == nil {
		t.Error("unknown output accepted")
	}
}

func TestFanSafety(t *testing.T) {
	// No disk temperature → full speed.
	root := fakeHwmon(t, "40000")
	f := newFan(t, root, `{}`)
	f.Save(context.Background(), store.FanSettings{Mode: "curve", PWM: "hwmon2/pwm1", MinPercent: 30, LowTemp: 35, HighTemp: 45})
	chip := filepath.Join(root, "class", "hwmon", "hwmon2")
	if read(chip, "pwm1") != "255" {
		t.Errorf("unknown temperature must mean full speed, pwm=%s", read(chip, "pwm1"))
	}
	// A sensor at its critical limit → full speed even if disks are cool.
	root = fakeHwmon(t, "95000")
	f = newFan(t, root, `{"smart_status":{"passed":true},"temperature":{"current":30}}`)
	f.Save(context.Background(), store.FanSettings{Mode: "curve", PWM: "hwmon2/pwm1", MinPercent: 30, LowTemp: 35, HighTemp: 45})
	if read(filepath.Join(root, "class", "hwmon", "hwmon2"), "pwm1") != "255" {
		t.Error("critical sensor must mean full speed")
	}
	// Emergency brake after a crash: outputs left in manual go to full.
	os.WriteFile(filepath.Join(root, "class", "hwmon", "hwmon2", "pwm1"), []byte("50"), 0o644)
	AllFansFull(root)
	if read(filepath.Join(root, "class", "hwmon", "hwmon2"), "pwm1") != "255" {
		t.Error("AllFansFull must set manual outputs to 255")
	}
}
