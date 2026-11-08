#!/usr/bin/env bash
# Boots the dev VM with the same constraints as the ReadyNAS (1 CPU, 1 GB RAM).
# Ports forwarded to your PC:
#   localhost:2222 -> ssh      (use vm/ssh.sh)
#   localhost:8443 -> nasd UI  (https://localhost:8443)
#   localhost:4445 -> SMB
# Quit: Ctrl-A then X   (or `sudo poweroff` inside the VM)
# Diagnostics: the serial console is also logged to vm/console.log, and the
# QEMU monitor listens on vm/monitor.sock (try: vm/monitor.sh "info status").
set -euo pipefail
cd "$(dirname "$0")"
[[ -f system.qcow2 ]] || { echo "run vm/setup.sh first"; exit 1; }
if pgrep -f "qemu-system-x86_64.*system.qcow2" >/dev/null; then
  echo "the VM is already running (pgrep -af qemu-system to find it)"; exit 1
fi

disks=()
for i in 1 2 3 4; do
  disks+=(-drive "file=disk$i.qcow2,if=none,id=d$i,format=qcow2"
          -device "ide-hd,drive=d$i,bus=ahci.$((i-1)),serial=NASDISK000$i,model=DEV-DISK-$i")
done

exec qemu-system-x86_64 \
  -machine q35,accel=kvm -boot strict=on -cpu host -smp 1 -m 1024 \
  -display none \
  -chardev stdio,id=con,mux=on,logfile=console.log,signal=off \
  -serial chardev:con -mon chardev=con,mode=readline \
  -monitor unix:monitor.sock,server=on,wait=off \
  -drive file=system.qcow2,if=none,id=sys,format=qcow2 \
  -device virtio-blk-pci,drive=sys,bootindex=0 \
  -drive file=seed.iso,if=virtio,format=raw,readonly=on \
  -device ahci,id=ahci \
  -device qemu-xhci,id=usb \
  "${disks[@]}" \
  -nic user,model=virtio-net-pci,hostfwd=tcp:127.0.0.1:2222-:22,hostfwd=tcp:127.0.0.1:8443-:8443,hostfwd=tcp:127.0.0.1:4445-:445
