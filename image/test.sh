#!/usr/bin/env bash
# Boots image/nas.raw the way the ReadyNAS will: from a USB disk (here a
# virtual one, larger than the image to check the first-boot growth), with
# no cloud-init seed. SSH on localhost:2223, UI on https://localhost:9443.
# The image itself is not modified (copy-on-write overlay).
set -euo pipefail
cd "$(dirname "$0")"
[[ -f nas.raw ]] || { echo "run image/build.sh first"; exit 1; }
rm -f build/test.qcow2
qemu-img create -q -f qcow2 -F raw -b "$PWD/nas.raw" build/test.qcow2 16G
exec qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 1 -m 1024 -display none -serial file:build/test-console.log \
  -device qemu-xhci,id=usb -drive file=build/test.qcow2,if=none,id=stick,format=qcow2 -device usb-storage,bus=usb.0,drive=stick,bootindex=0 \
  -nic user,model=e1000,hostfwd=tcp:127.0.0.1:2223-:22,hostfwd=tcp:127.0.0.1:9443-:8443
