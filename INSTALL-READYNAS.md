# Installing on the ReadyNAS Ultra 4 (RNDU4000, sticker RND-4A, Atom D410)

No password from the old OS (RAIDiator) is needed to replace it, but having
admin access (and root SSH through the EnableRootSSH add-on) makes it simple.

How the ReadyNAS starts: the BIOS boots a small **internal USB flash**
(125 MB, FAT16) holding Netgear's boot loader (SYSLINUX 3.31, patched) and
RAIDiator's kernel. RAIDiator itself then runs from the data disks (a RAID1
`md0` on partition 1 of each disk).

Our system lives on a **USB SSD or USB stick** (≥ 16 GB). We add our kernel
next to RAIDiator's on the flash and make it the default: the boot loader then
starts Debian from the USB disk. The four bays stay for data.

## What you need
- a USB SSD (recommended) or a good USB stick, **16 GB or more**
- a network cable from the NAS to your router or modem (DHCP)
- this PC, and the NAS admin (= root) password

## 1. Build and test the image (on the PC)
```
vm/setup.sh          # once: downloads and verifies Debian 13 (if not done yet)
image/build.sh       # ~10 min: builds image/nas.raw (Debian + nasd + drivers)
image/test.sh        # optional: boots it as a USB disk in QEMU (SSH :2223, UI :9443)
```
`image/build.sh` also creates:
- `image/id_ed25519`: the SSH key of the NAS administrator account (`nas`),
- `image/credentials.txt`: a console password (serial console only; SSH uses the key),
- `image/build/flash/`: the three files for the flash (`nas.krn`, `nas.ird`, `syslinux.cfg`).

Keep the key and the password: they are your way into the system (never committed to git).

## 2. Write the USB disk
```
sudo image/write.sh              # lists the USB disks
sudo image/write.sh /dev/sdX     # erases sdX and writes the system
```
The script refuses anything that isn't a USB disk and asks you to type its name.

## 3. Root SSH into RAIDiator
RAIDiator's SSH is from 2007: allow its old algorithms for this host only.
```
# ~/.ssh/config
Host datanas
    HostName 192.168.0.6
    User root
    KexAlgorithms +diffie-hellman-group14-sha1
    HostKeyAlgorithms +ssh-rsa
    PubkeyAcceptedAlgorithms +ssh-rsa
```
(Its web interface only speaks TLS 1.0; modern browsers refuse it.)

## 4. Back up the flash
RAIDiator detaches the flash after booting. Re-attach it (read-only operation):
```
ssh datanas 'echo 2-1:1.0 > /sys/bus/usb/drivers/usb-storage/bind; sleep 3; grep sde /proc/partitions'
ssh datanas 'dd if=/dev/sde bs=1M' > image/readynas-flash.img      # 125,304,832 bytes
```
RAIDiator then mounts it on `/USB/USB_FLASH_1`.

## 5. Put our kernel on the flash
```
cd image/build/flash
tar cf - nas.krn nas.ird syslinux.cfg | ssh datanas 'cd /USB/USB_FLASH_1 && tar xf - && sync && md5sum nas.krn nas.ird syslinux.cfg'
md5sum nas.krn nas.ird syslinux.cfg     # must match the line above
```
The new `syslinux.cfg` boots `NAS` by default and keeps every RAIDiator entry
(Normal, OSReinstall, ...).

## 6. Start our system
1. Shut RAIDiator down (`ssh datanas poweroff`).
2. Plug the USB disk into a **back** USB port (keep the front one for USB copy).
3. Power on. First boot takes 2-3 minutes: the system partition grows to
   fill the disk, and the flash is found and mounted on `/boot/flash`.

## 7. Find it and sign in
- Web interface: **https://<ip>:8443**: the IP is in the router/modem's DHCP
  table (`nas.local` works only when the PC is on the same network segment).
  Accept the certificate warning, then choose the **admin password**.
- SSH: `ssh -i image/id_ed25519 nas@<ip>`
- Updates of nasd from this PC: `make deploy NAS=nas@<ip>`

Kernel updates keep working: a hook (`nas-flash-sync`) copies every new kernel
and initramfs to the flash.

## 8. Move the system onto the disks (like RAIDiator)
RAIDiator kept its system on partition 1 of every disk (a RAID 1 with 4
copies) and its swap on partition 2. Once the data volume is adopted, reuse
them: the NAS then survives a dead disk, and the USB disk becomes a spare.
```
scp -i image/id_ed25519 image/nas-system-to-disks nas@<ip>:
ssh -i image/id_ed25519 nas@<ip> 'sudo install -m 755 nas-system-to-disks /usr/local/sbin/ && cat /proc/mdstat'
ssh -t -i image/id_ed25519 nas@<ip> 'sudo nas-system-to-disks /dev/md127 /dev/md126'   # RAID 1 system, swap
```
It renames the arrays to `system` and `swap`, copies the running system,
keeps logs in memory (so the disks can sleep), removes GRUB (the flash boots
the kernel) and rewrites the flash's boot menu: `NAS` (disks, default),
`NASUSB` (the USB disk, spare), `MemoryTest`. RAIDiator's files and entries
are removed from the flash (they are in `readynas-flash.img`).

A replacement disk then gets the same three partitions and joins the
system, swap and data arrays (Volumes & Disks → the volume → Add disk).

## 9. Hardware checks
In the web interface:
- **Volumes & Disks**: the old RAIDiator arrays (md0/md1/md2) appear as old
  data: delete / erase them, then create your volume. Check that bays 1-4
  match the physical bays (Locate).
- **System Settings → Hardware**: fan and temperatures (IT8721F chip, `it87`
  driver), front display, Backup button (Netgear's own `pvgpio` driver in
  RAIDiator: still to be mapped).
- Network: both ports, bonding, Wake-on-LAN, power schedule (power-on alarm).

## Going back to RAIDiator
Write the backup back on the flash (from our system):
```
ssh -i image/id_ed25519 nas@<ip> 'sudo umount /boot/flash; sudo dd of=$(ls /dev/disk/by-id/usb-SMI_USB_DISK*0:0) bs=1M conv=fsync' < image/readynas-flash.img
```
(or simply change `default NAS` to `default Normal` in `/boot/flash/syslinux.cfg`),
then unplug the USB disk. As long as the data disks weren't erased, RAIDiator
starts as before. Otherwise its boot menu (Reset + Power) offers **OS
reinstall**, which also resets the admin password to `netgear1`.

## Giving the system to others
```
image/build.sh --dist      # image/nas-dist.raw: nothing personal inside
```
- **No key, no password**: the `nas` account is locked. After installing,
  the owner adds their own public key in **System Settings → Services →
  Administrator SSH access** (admin password asked).
- **Own identity per installation**: SSH host keys are generated at first
  boot, nasd's TLS certificate and data at first start (never shared).
- **Setup code**: until the admin account exists, creating it requires the
  code shown on the front display (and written in `journalctl -u nasd`), so
  the first visitor on the network can't take the NAS over.
- **License**: GPL-3.0 (LICENSE). Give the source of nasd with the image,
  and offer the Debian source of the packages it contains (NOTICE).
  Never include NETGEAR files (the flash backup, `*.tlz`, `*.ko`).
- **Lost access**: `ssh … 'sudo systemctl stop nasd && sudo nasd -reset-admin; sudo systemctl start nasd'`
  prints a new admin password; on the ReadyNAS, holding Backup 10 s does the same.
