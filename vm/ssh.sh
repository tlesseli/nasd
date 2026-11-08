#!/usr/bin/env bash
# SSH into the dev VM: vm/ssh.sh [command...]
cd "$(dirname "$0")"
exec ssh -i id_ed25519 -o IdentitiesOnly=yes -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR nas@127.0.0.1 "$@"
