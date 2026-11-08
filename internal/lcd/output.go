package lcd

import (
	"os"
	"sync"
)

// Simulated is the output when no display driver is found: the engine
// still computes frames (visible in the web interface).
type Simulated struct{}

func (Simulated) Show(string, string, bool) error { return nil }
func (Simulated) Name() string                    { return "simulated (no display driver found)" }

// CharLCD writes to Linux's character display device (/dev/lcd, created by
// the charlcd / hd44780 drivers). It understands escape sequences:
//
//	ESC [ L x<col> y<row> ;   move the cursor
//	ESC [ L +  / ESC [ L -    backlight on / off
//
// Whether the ReadyNAS display uses this driver is to be confirmed on the
// hardware; another Output can be added without touching the engine.
type CharLCD struct {
	Path string
	mu   sync.Mutex
}

func (c *CharLCD) Name() string { return "charlcd (" + c.Path + ")" }

func (c *CharLCD) Show(line1, line2 string, backlight bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.OpenFile(c.Path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	bl := "\x1b[L-"
	if backlight {
		bl = "\x1b[L+"
	}
	_, err = f.WriteString("\x1b[Lx0y0;" + line1 + "\x1b[Lx0y1;" + line2 + bl)
	return err
}

// Detect picks the output: /dev/lcd if present, otherwise simulated.
func Detect() Output {
	if fi, err := os.Stat("/dev/lcd"); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		return &CharLCD{Path: "/dev/lcd"}
	}
	return Simulated{}
}

// ShowOnce writes one message with the backlight on (startup/shutdown,
// from systemd while nasd isn't running).
func ShowOnce(out Output, line1, line2 string) error {
	return out.Show(fit(line1), fit(line2), true)
}
