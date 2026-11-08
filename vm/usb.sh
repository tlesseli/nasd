#!/usr/bin/env bash
# Plug / unplug a virtual USB disk into the running dev VM (hot-plug, like a
# real USB stick).
#   vm/usb.sh plug [name] [size]   e.g. vm/usb.sh plug usb1 1G
#   vm/usb.sh unplug [name]
# The disk image (vm/<name>.qcow2) is kept between plugs, like a real disk.
set -euo pipefail
cd "$(dirname "$0")"
action=${1:-}; name=${2:-usb1}; size=${3:-1G}
case "$action" in
  plug)
    [[ -f $name.qcow2 ]] || qemu-img create -q -f qcow2 "$name.qcow2" "$size"
    ./monitor.sh "drive_add 0 if=none,id=$name,file=$PWD/$name.qcow2,format=qcow2"
    ./monitor.sh "device_add usb-storage,bus=usb.0,drive=$name,id=dev-$name,serial=USB-$name"
    echo "plugged $name"
    ;;
  unplug)
    ./monitor.sh "device_del dev-$name"
    echo "unplugged $name"
    ;;
  *) echo "usage: $0 plug|unplug [name] [size]"; exit 1 ;;
esac
