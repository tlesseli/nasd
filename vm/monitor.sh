#!/usr/bin/env bash
# Send a command to the QEMU monitor of the running dev VM, e.g.
#   vm/monitor.sh "info status"     vm/monitor.sh "info registers"
cd "$(dirname "$0")"
printf '%s\n' "$*" | timeout 5 socat - UNIX-CONNECT:monitor.sock 2>/dev/null \
  || printf '%s\n' "$*" | timeout 5 nc -U -q1 monitor.sock
