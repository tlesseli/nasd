#!/usr/bin/env bash
# Writes image/nas.raw to a USB SSD or stick. EVERYTHING ON THAT DEVICE IS ERASED.
#   sudo image/write.sh /dev/sdX
# Refuses anything that isn't a USB disk, and asks you to type its name.
set -euo pipefail
cd "$(dirname "$0")"
DEV=${1:-}
[[ $EUID -eq 0 ]] || { echo "run with sudo"; exit 1; }
[[ -f nas.raw ]] || { echo "image/nas.raw missing: run image/build.sh first"; exit 1; }
if [[ -z $DEV ]]; then
  echo "USB disks connected:"; lsblk -d -o NAME,SIZE,MODEL,TRAN | awk 'NR==1 || $NF=="usb"'
  echo "usage: sudo $0 /dev/sdX"; exit 1
fi
[[ -b $DEV ]] || { echo "$DEV is not a block device"; exit 1; }
NAME=$(basename "$DEV")
[[ $(lsblk -dno TRAN "$DEV") == usb ]] || { echo "refusing: $DEV is not a USB disk"; exit 1; }
if lsblk -no MOUNTPOINTS "$DEV" | grep -qE '^/$|^/boot|^/home'; then echo "refusing: $DEV holds this PC's system"; exit 1; fi
NEED=$(stat -c %s nas.raw); HAVE=$(lsblk -bdno SIZE "$DEV")
(( HAVE >= NEED )) || { echo "refusing: $DEV is smaller than the image"; exit 1; }
lsblk -o NAME,SIZE,MODEL,FSTYPE,LABEL "$DEV"
read -rp "Everything on $DEV ($(lsblk -dno MODEL "$DEV" | xargs), $(lsblk -dno SIZE "$DEV" | xargs)) will be erased. Type $NAME to continue: " ANSWER
[[ $ANSWER == "$NAME" ]] || { echo "cancelled"; exit 1; }
for p in $(lsblk -lno PATH "$DEV" | tail -n +2); do umount "$p" 2>/dev/null || true; done
dd if=nas.raw of="$DEV" bs=4M conv=fsync status=progress
sync
echo "done: $DEV holds the NAS system. Plug it into the ReadyNAS front USB port."
