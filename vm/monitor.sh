#!/usr/bin/env bash
# Send a command to the QEMU monitor of the running dev VM, e.g.
#   vm/monitor.sh "info status"     vm/monitor.sh "info usb"
cd "$(dirname "$0")"
printf '%s\n' "$*" | timeout 5 socat - UNIX-CONNECT:monitor.sock 2>/dev/null \
  | tr -d '\r' | sed 's/\x1b\[[0-9;]*[A-Za-z]//g' \
  | grep -v -e '^QEMU .* monitor' -e '^(qemu)' -e '^$' || true
