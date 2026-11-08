#!/usr/bin/env bash
# Prepares the files for the ReadyNAS internal flash in image/build/flash/:
#   nas.krn, nas.ird   our kernel and initramfs, taken from image/nas.raw
#   syslinux.cfg       Netgear's boot menu with our "NAS" entry as default
#                      (RAIDiator's entries are kept, for going back)
# Reads the image with debugfs: no root, no mounting.
set -euo pipefail
cd "$(dirname "$0")"
[[ -f nas.raw ]] || { echo "build the image first (image/build.sh)"; exit 1; }

# The root partition: its start (for debugfs) and its PARTUUID (for the kernel).
read -r START PARTUUID < <(sfdisk --json nas.raw | python3 -c '
import json, sys
for p in json.load(sys.stdin)["partitiontable"]["partitions"]:
    if p["type"] == "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709":  # Linux root (x86-64)
        print(p["start"], p["uuid"].lower())')
ROOTFS="nas.raw?offset=$((START * 512))"

VER=$(debugfs -R 'ls -p /boot' "$ROOTFS" 2>/dev/null | awk -F/ '$6 ~ /^vmlinuz-/ {sub("vmlinuz-", "", $6); print $6}' | sort -V | tail -1)
[[ -n $VER ]] || { echo "no kernel found in the image"; exit 1; }

mkdir -p build/flash
rm -f build/flash/nas.krn build/flash/nas.ird
debugfs -R "dump /boot/vmlinuz-$VER build/flash/nas.krn" "$ROOTFS" 2>/dev/null
debugfs -R "dump /boot/initrd.img-$VER build/flash/nas.ird" "$ROOTFS" 2>/dev/null
[[ -s build/flash/nas.krn && -s build/flash/nas.ird ]] || { echo "could not extract the kernel"; exit 1; }

# Netgear's SYSLINUX is version 3.31: 8.3 file names only, and no "prompt"
# (its patched menu then waits forever for the front-panel buttons).
cat > build/flash/syslinux.cfg <<EOF
serial 0 9600 0
default NAS

# Our system: kernel and initramfs here, everything else on the USB disk.
label NAS
kernel nas.krn
append initrd=nas.ird root=PARTUUID=$PARTUUID ro console=tty0 console=ttyS0,9600n8 consoleblank=0 hpet=disable

# RAIDiator (original entries, chosen from the front boot menu).
label Normal
kernel kernel
append initrd=initrd.gz reason=normal

label FactoryDefault
kernel kernel
append initrd=initrd.gz reason=factory

label OSReinstall
kernel kernel
append initrd=initrd.gz reason=os_reinstall

label TechSupport
kernel kernel
append initrd=initrd.gz reason=diag

label SkipVolCheck
kernel kernel
append initrd=initrd.gz reason=skip_fsck

label MemoryTest
kernel memtest

label TestDisks
kernel kernel
append initrd=initrd.gz reason=test_disks
EOF
echo ">> flash files for kernel $VER (root PARTUUID=$PARTUUID):"
ls -l build/flash
