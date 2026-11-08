# readynas: a home-made OS for a ReadyNAS (Atom D410, 1 GB RAM, 4 bays)

Debian 13 + `nasd`, a single Go binary that serves a Svelte web UI and drives
standard Linux tools (`lsblk`, `smartctl`, later `mdadm` and `samba`).
LAN only. Learning project.

## Layout
```
cmd/nasd/          entry point (flags, HTTPS server)
internal/api/      HTTP endpoints, login, CSRF guard
internal/auth/     password hashing (PBKDF2), sessions, login throttle
internal/disks/    lsblk + smartctl parsing
internal/store/    state.json (users, later shares)
internal/system/   uptime / memory / load from /proc
internal/tlsutil/  self-signed HTTPS certificate
web/               Svelte UI, embedded into the binary
vm/                QEMU dev VM: 1 CPU, 1 GB RAM, 4 SATA data disks
deploy/            systemd unit
```

## Daily workflow
```
vm/run.sh          # start the VM in its own terminal (Ctrl-A X to quit)
make deploy        # build UI + binary, install & restart nasd in the VM
make test          # unit tests
make logs          # follow nasd logs
vm/ssh.sh          # shell in the VM (tools live in /usr/sbin: use sudo)
vm/setup.sh        # reset the VM to a blank state (wipes everything)
cd web && npm run dev   # UI hot reload on http://localhost:5173 (API -> VM)
```
UI: https://localhost:8443 (accept the self-signed certificate warning once).

## Roadmap
1. [x] Dev VM with 4 disks
2. [x] nasd skeleton: HTTPS, first-run admin setup, login, disks + SMART, dashboard
3. [x] Volumes: RAID 0/1/5/6/10 (mdadm), hot spare, replace disk, expand RAID 5/6
4. [x] Shares + users (Samba SMB2+), guest access, recycle bin, Time Machine, discovery
5b. [x] Backups: rsync jobs to/from USB, SSH, rsync server; schedule; rsync service; allowed hosts
5. [x] Alerts & health: email alerts (contacts, SMTP, test, per-event switches), temperatures/fans,
       SMART details, system logs, volume maintenance (scheduled scrub + parity repair, fs check)
5c. [ ] Power & system: disk spin-down, power schedule, UPS (NUT), Wake-on-LAN; hostname, time/NTP,
       network (static IP, bonding, MTU, with rollback), reboot/shutdown, OS updates
5d. [ ] Users & USB: private home share per user, disk quotas, hidden shares, copy USB content on connection
6. [ ] Real ReadyNAS: install, Backup button, locate a disk (bay LEDs), fan control

Inspired by the original *ReadyNAS for Home RAIDiator 4.2 Software Manual* (2011). Deliberately
left out: NETGEAR cloud services, iSCSI, printers, AFP, add-ons.
