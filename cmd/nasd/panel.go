package main

// The ReadyNAS front panel (display, bay LEDs, Backup button), when nasd
// runs on one: see internal/readynas.

import (
	"context"
	"fmt"
	"log"
	"time"

	"readynas/internal/api"
	"readynas/internal/disks"
	"readynas/internal/hardware"
	"readynas/internal/lcd"
	"readynas/internal/readynas"
)

// openPanel returns the front panel on a ReadyNAS, nil elsewhere.
func openPanel() *readynas.Panel {
	if ok, why := readynas.Detect(); !ok {
		if why != "not a NETGEAR ReadyNAS" {
			log.Printf("front panel: %s", why)
		}
		return nil
	}
	p, err := readynas.Open()
	if err != nil {
		log.Printf("ReadyNAS front panel: %v", err)
		return nil
	}
	return p
}

// displayFor picks the display: a standard Linux character display if the
// kernel has one, else the ReadyNAS panel, else a simulated one (the web
// interface still shows what it would display).
func displayFor(p *readynas.Panel) lcd.Output {
	out := lcd.Detect()
	if _, simulated := out.(lcd.Simulated); simulated && p != nil {
		return p.Display()
	}
	return out
}

// bayOf: the bay number of a disk (sdc -> 3), 0 if unknown.
func bayOf(ctx context.Context, dk *disks.Service, disk string) int {
	list, _ := dk.List(ctx)
	for _, d := range list {
		if d.Name == disk {
			return d.Bay
		}
	}
	return 0
}

// wirePanel connects the panel to Locate, the bay LEDs, the Backup button
// and the admin password reset.
func wirePanel(ctx context.Context, p *readynas.Panel, dk *disks.Service, loc *hardware.Locator, btn *hardware.Button,
	display *lcd.Engine, srv *api.Server) {
	// Locate: the chipset blinks the bay's LED by itself.
	loc.LED = func(disk string, blink bool) bool {
		pin, ok := readynas.BayLED[bayOf(context.Background(), dk, disk)]
		if !ok {
			return false
		}
		if blink {
			p.Blink(pin, true)
		} else {
			p.LED(pin, true)
		}
		return true
	}
	// Bay LEDs: lit when the bay holds a disk (RAIDiator did the same).
	go func() {
		for {
			list, _ := dk.List(ctx)
			present := map[int]bool{}
			for _, d := range list {
				present[d.Bay] = true
			}
			for bay, pin := range readynas.BayLED {
				if _, blinking := p.LEDState(pin); !blinking { // not while locating
					p.LED(pin, present[bay])
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	}()
	// Both buttons are chipset pins. The Backup button runs the backup jobs.
	// Holding the recessed Reset button at the back (a paperclip) resets the
	// admin password and shows the new one on the display: only someone at
	// the NAS can do it, like the reset button of most NAS boxes.
	btn.GPIO = func() bool { return p.Pressed(readynas.BackupButton) }
	go hardware.PinWatch{
		Read: func() bool { return p.Pressed(readynas.ResetButton) },
		Hold: hardware.HoldReset,
		OnHolding: func(left int) {
			display.Notice("Keep holding to", fmt.Sprintf("reset admin: %2ds", left), 1500*time.Millisecond)
		},
		OnLong: func() {
			pw, err := srv.ResetAdminPassword()
			if err != nil {
				display.Notice("Reset failed:", err.Error(), time.Minute)
				return
			}
			display.Notice("Admin password:", pw, 10*time.Minute)
		},
	}.Run(ctx)
}
