# Installing on the ReadyNAS (RND-4A / NVX, Atom D410)

No password from the old OS (RAIDiator) is needed: we don't log into it, we
replace it. The new system lives on a **USB SSD or USB stick** (≥ 16 GB), so
the four bays stay for data.

## What you need
- a USB SSD (recommended) or a good USB stick, **16 GB or more**
- a network cable from the NAS to your router (DHCP)
- this PC

## 1. Build and test the image (on the PC)
```
vm/setup.sh          # once: downloads and verifies Debian 13 (if not done yet)
image/build.sh       # ~10 min: builds image/nas.raw (Debian + nasd + drivers)
image/test.sh        # optional: boots it as a USB disk in QEMU (SSH :2223, UI :9443)
```
`image/build.sh` also creates:
- `image/id_ed25519`: the SSH key of the NAS administrator account (`nas`),
- `image/credentials.txt`: a console password (serial console only; SSH uses the key).

Keep both: they are your way into the system (never committed to git).

## 2. Write it on the USB disk
```
sudo image/write.sh              # lists the USB disks
sudo image/write.sh /dev/sdX     # erases sdX and writes the system
```
The script refuses anything that isn't a USB disk and asks you to type its name.

## 3. Start the NAS from it
1. Power the NAS off. Leave the disks in place (their old data is erased later, from the UI).
2. Plug the USB disk into the **front** USB port.
3. **Hold the Backup button** while pressing Power, and keep holding it until
   the disk LEDs start blinking (~10 s). This makes the ReadyNAS boot from the
   front USB port instead of its internal flash.
4. Wait 2-3 minutes (first boot: the system partition grows to fill the disk).

If it doesn't boot from the USB disk (old OS starts, or nothing happens), stop
there: we'll use the serial port at the back or the internal VGA header.

## 4. Find it and sign in
- Web interface: **https://nas.local:8443** (or the IP your router gave it,
  listed in the router's DHCP table). Accept the certificate warning, then
  choose the **admin password**.
- SSH: `ssh -i image/id_ed25519 nas@nas.local`
- Updates of nasd from this PC: `make deploy NAS=nas@nas.local`

## 5. Back up the internal flash (before anything else)
The ReadyNAS boots from a small internal flash module (~128-256 MB). Copy it
once: it lets you go back to RAIDiator at any time, and it shows which
drivers RAIDiator used (front display, buttons).
```
ssh -i image/id_ed25519 nas@nas.local 'lsblk -d -o NAME,SIZE,MODEL,TRAN'   # the small USB disk
ssh -i image/id_ed25519 nas@nas.local 'sudo dd if=/dev/sdX bs=1M' > readynas-flash.img
```

## 6. Boot without the Backup button
Two ways, to decide together once the flash is backed up:
- **BIOS boot order** (needs a screen: VGA header or serial adapter), or
- **a small bootloader on the internal flash** that starts the system on the
  USB disk (no screen needed; reversible with the backup of step 5).

## 7. Hardware checks
In the web interface:
- **Volumes & Disks**: the old RAIDiator arrays appear as volumes or "old data":
  delete / erase them, then create your volume. Check that bays 1-4 match the
  physical bays (Locate).
- **System Settings → Hardware**: Learn the Backup button, test Locate, check
  the fan and temperatures, look at the front display.
- Over SSH, if sensors are missing: `sudo sensors-detect` (the board's chip).
- Network: both ports, bonding, Wake-on-LAN, power schedule (power-on alarm).

## Going back to RAIDiator
Unplug the USB disk: the NAS boots its internal flash again (as long as step 6
wasn't done; otherwise write `readynas-flash.img` back to the flash first).
RAIDiator's boot menu (Reset + Power) then offers **OS reinstall**, which resets
its admin password to `netgear1`.
