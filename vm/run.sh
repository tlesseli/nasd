#!/usr/bin/env bash
# Boots the dev VM with the same constraints as the ReadyNAS (1 CPU, 1 GB RAM).
# Ports forwarded to your PC:
#   localhost:2222 -> ssh      (use vm/ssh.sh)
#   localhost:8443 -> nasd UI  (https://localhost:8443)
#   localhost:4445 -> SMB
# Quit: Ctrl-A then X   (or `sudo poweroff` inside the VM)
set -euo pipefail
cd "$(dirname "$0")"
[[ -f system.qcow2 ]] || { echo "run vm/setup.sh first"; exit 1; }

disks=()
for i in 1 2 3 4; do
  disks+=(-drive "file=disk$i.qcow2,if=none,id=d$i,format=qcow2"
          -device "ide-hd,drive=d$i,bus=ahci.$((i-1)),serial=NASDISK000$i,model=DEV-DISK-$i")
done

exec qemu-system-x86_64 \
  -machine q35,accel=kvm -boot strict=on -cpu host -smp 1 -m 1024 \
  -nographic \
  -drive file=system.qcow2,if=none,id=sys,format=qcow2 \
  -device virtio-blk-pci,drive=sys,bootindex=0 \
  -drive file=seed.iso,if=virtio,format=raw,readonly=on \
  -device ahci,id=ahci \
  "${disks[@]}" \
  -nic user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:2222-:22,hostfwd=tcp:127.0.0.1:8443-:8443,hostfwd=tcp:127.0.0.1:4445-:445
