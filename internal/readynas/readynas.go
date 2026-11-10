// Package readynas drives the front panel of the ReadyNAS Ultra (Pineview
// Atom D410 board with an Intel ICH9): bay LEDs, the Backup button and the
// 2x16 character display. RAIDiator did it with Netgear's own kernel module
// (pvgpio.ko); everything here comes from reading that module:
//
//   - all of them are general-purpose pins of the ICH9 chipset, controlled
//     through its GPIO registers in I/O space (GPIOBASE, from the LPC
//     bridge's PCI config, offset 0x48),
//   - LEDs (bank 1): Backup button 22, Power button 28, disk1..4 on 16, 20,
//     6, 7 (checked on the hardware: Netgear's table lists the two button
//     LEDs first).
//     A LED is lit when its pin is low; the chipset can blink it by itself
//     (GPO_BLINK register),
//   - buttons (bank 1, inputs, low when pressed): Backup on pin 4, the
//     recessed Reset button at the back on pin 5 (checked on the hardware),
//   - display (bank 2 = pins 32..63): an HD44780-compatible LCD in 4-bit
//     mode. E, RS, RW on pins 32, 33, 34, data D4..D7 on 36..39, backlight
//     on 48.
//
// Registers are reached through /dev/port (root only), with read-modify-
// write of single bits, so pins we don't own keep their state.
package readynas

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// ICH9 GPIO registers, offsets from GPIOBASE.
const (
	regLevel  = 0x0c // GP_LVL: pins 0..31
	regBlink  = 0x18 // GPO_BLINK: pins 0..31
	regLevel2 = 0x38 // GP_LVL2: pins 32..63
)

// Pins (bank 1 bits for LEDs and buttons, bank 2 bits for the display).
var (
	BayLED       = map[int]int{1: 16, 2: 20, 3: 6, 4: 7}
	BackupLED    = 22
	PowerLED     = 28
	BackupButton = 4 // front, runs the backup jobs
	ResetButton  = 5 // back, recessed: holding it resets the admin password
)

const (
	lcdE, lcdRS, lcdRW = 0, 1, 2
	lcdD4              = 4  // D4..D7 = bits 4..7
	lcdBacklight       = 16 // pin 48
)

// Detect reports whether this is a machine whose front panel we know: a
// NETGEAR ReadyNAS of the Ultra 2/4 family. The pins are written directly in
// the chipset, so a wrong guess could drive pins that do something else:
// all three must match, otherwise the panel is left alone.
//   - DMI: NETGEAR / ReadyNAS,
//   - the LPC bridge is an Intel ICH9 (8086:2918), where the pins live,
//   - the CPU is an Atom D410 (the Ultra 6 has a D510 and other pins).
//
// NASD_PANEL=off (environment) disables the panel whatever the machine.
func Detect() (bool, string) {
	if os.Getenv("NASD_PANEL") == "off" {
		return false, "disabled (NASD_PANEL=off)"
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return strings.TrimSpace(string(b)) }
	if read("/sys/class/dmi/id/sys_vendor") != "NETGEAR" || !strings.HasPrefix(read("/sys/class/dmi/id/product_name"), "ReadyNAS") {
		return false, "not a NETGEAR ReadyNAS"
	}
	if read("/sys/bus/pci/devices/0000:00:1f.0/vendor") != "0x8086" || read("/sys/bus/pci/devices/0000:00:1f.0/device") != "0x2918" {
		return false, "ReadyNAS with an unknown chipset: front panel left alone"
	}
	if cpu := read("/proc/cpuinfo"); !strings.Contains(cpu, "Atom(TM) CPU D410") {
		return false, "ReadyNAS model not supported (Ultra 2/4 only): front panel left alone"
	}
	return true, "ReadyNAS Ultra 2/4"
}

// Panel gives access to the front panel pins.
type Panel struct {
	mu   sync.Mutex
	port *os.File
	base int64
}

// Open finds GPIOBASE and opens /dev/port.
func Open() (*Panel, error) {
	cfg, err := os.ReadFile("/sys/bus/pci/devices/0000:00:1f.0/config")
	if err != nil || len(cfg) < 0x50 {
		return nil, fmt.Errorf("cannot read the LPC bridge configuration: %v", err)
	}
	base := int64(binary.LittleEndian.Uint32(cfg[0x48:]) & 0xff80)
	if cfg[0x4c]&0x10 == 0 || base == 0 {
		return nil, errors.New("the chipset's GPIO block is disabled")
	}
	f, err := os.OpenFile("/dev/port", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &Panel{port: f, base: base}, nil
}

func (p *Panel) Close() error { return p.port.Close() }

func (p *Panel) read(reg int64) uint32 {
	var b [4]byte
	p.port.ReadAt(b[:], p.base+reg)
	return binary.LittleEndian.Uint32(b[:])
}

func (p *Panel) write(reg int64, v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	p.port.WriteAt(b[:], p.base+reg)
}

func (p *Panel) setBit(reg int64, bit int, on bool) {
	v := p.read(reg)
	if on {
		v |= 1 << bit
	} else {
		v &^= 1 << bit
	}
	p.write(reg, v)
}

// LED switches a LED on or off (and stops its blinking).
func (p *Panel) LED(pin int, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setBit(regBlink, pin, false)
	p.setBit(regLevel, pin, !on) // lit when low
}

// Blink makes the chipset blink a LED by itself (or stops it).
func (p *Panel) Blink(pin int, on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on {
		p.setBit(regLevel, pin, false)
	}
	p.setBit(regBlink, pin, on)
}

// LEDState: lit, blinking.
func (p *Panel) LEDState(pin int) (lit, blinking bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.read(regLevel)&(1<<pin) == 0, p.read(regBlink)&(1<<pin) != 0
}

// Pressed reports whether a button pin is pressed (pulled low).
func (p *Panel) Pressed(pin int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.read(regLevel)&(1<<pin) == 0
}

// ---------------------------------------------------------------- display

// Timing copied from Netgear's driver: about 0.5 ms around each edge of
// the E pulse. Slow, but the display then never misses a nibble.
const lcdDelay = 500 * time.Microsecond

// nibble sends 4 bits: data on D4..D7 with E low, then E high, then low.
func (p *Panel) nibble(rs bool, n byte) {
	v := p.read(regLevel2)
	v &^= 1<<lcdE | 1<<lcdRS | 1<<lcdRW | 0xf<<lcdD4
	if rs {
		v |= 1 << lcdRS
	}
	v |= uint32(n&0xf) << lcdD4
	p.write(regLevel2, v)
	time.Sleep(lcdDelay)
	p.write(regLevel2, v|1<<lcdE)
	time.Sleep(lcdDelay)
	p.write(regLevel2, v)
	time.Sleep(lcdDelay)
}

func (p *Panel) send(rs bool, b byte) {
	p.nibble(rs, b>>4)
	p.nibble(rs, b)
}

// LCDInit runs Netgear's start-up sequence: the standard wake-up (three
// times "8-bit mode", then 4-bit), then 4-bit/2 lines (0x2C), display off,
// clear, left-to-right, display on without cursor.
func (p *Panel) LCDInit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, step := range []struct {
		n    byte
		wait time.Duration
	}{{3, 4100 * time.Microsecond}, {3, 100 * time.Microsecond}, {3, 4100 * time.Microsecond}, {2, 100 * time.Microsecond}} {
		p.nibble(false, step.n)
		time.Sleep(step.wait)
	}
	for _, b := range []byte{0x2c, 0x08, 0x01, 0x06, 0x0c} {
		p.send(false, b)
		time.Sleep(2 * time.Millisecond) // "clear" needs 1.6 ms
	}
}

// LCDPark leaves the display the way RAIDiator did before every restart
// and shutdown (/etc/init.d/reboot: lcd_backlight=0, then lcd_reset):
// backlight off, then the full start-up sequence (cleared, 4-bit mode).
// Netgear's boot loader on the flash expects exactly that: left in any
// other state, it stops at the next start with a blinking cursor.
func (p *Panel) LCDPark() {
	p.mu.Lock()
	p.setBit(regLevel2, lcdBacklight, true) // off (lit when low)
	p.mu.Unlock()
	p.LCDInit()
	p.mu.Lock()
	p.setBit(regLevel2, lcdBacklight, true)
	p.mu.Unlock()
}

// Release parks the display (see LCDPark); it needs LCDInit again afterwards.
func (d *Display) Release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.p.LCDPark()
	d.ready = false
}

// LCDBacklight switches the backlight (lit when its pin is low).
func (p *Panel) LCDBacklight(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setBit(regLevel2, lcdBacklight, !on)
}

// LCDShow writes both lines (16 characters each) and sets the backlight.
func (p *Panel) LCDShow(line1, line2 string, backlight bool) {
	p.mu.Lock()
	for row, text := range []string{line1, line2} {
		p.send(false, 0x80|byte(row*0x40)) // cursor to the start of the row
		for i := 0; i < 16; i++ {
			c := byte(' ')
			if i < len(text) {
				c = text[i]
			}
			p.send(true, c)
		}
	}
	p.mu.Unlock()
	p.LCDBacklight(backlight)
}

// Display adapts the panel to nasd's display engine (lcd.Output): the
// display is initialized once, and redrawn only when the text changes
// (a redraw takes ~0.1 s at the driver's timing).
type Display struct {
	p      *Panel
	mu     sync.Mutex
	ready  bool
	l1, l2 string
}

func (p *Panel) Display() *Display { return &Display{p: p} }

func (d *Display) Name() string { return "ReadyNAS front display (chipset GPIO)" }

func (d *Display) Show(line1, line2 string, backlight bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.ready {
		d.p.LCDInit()
		d.ready = true
	} else if line1 == d.l1 && line2 == d.l2 {
		d.p.LCDBacklight(backlight)
		return nil
	}
	d.p.LCDShow(line1, line2, backlight)
	d.l1, d.l2 = line1, line2
	return nil
}
