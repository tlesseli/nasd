#!/usr/bin/env bash
# Creates the development VM that mimics the ReadyNAS:
#   - Debian 13 cloud image as system disk (virtio, /dev/vda)
#   - 4 data disks on a SATA/AHCI controller (/dev/sda..sdd), like the 4 bays
# Re-running it resets the VM to a clean state (data disks are wiped).
set -euo pipefail
cd "$(dirname "$0")"

IMG_URL=https://cloud.debian.org/images/cloud/trixie/latest
IMG=debian-13-genericcloud-amd64.qcow2
DATA_DISK_SIZE=2G

# 1. Base image (downloaded once, verified against Debian's SHA512SUMS)
if [[ ! -f $IMG ]]; then
  echo ">> downloading $IMG"
  curl -fSL -o "$IMG.part" "$IMG_URL/$IMG"
  curl -fsSL "$IMG_URL/SHA512SUMS" | grep " $IMG\$" | sed "s/$IMG\$/$IMG.part/" | sha512sum -c -
  mv "$IMG.part" "$IMG"
fi

# 2. Dedicated SSH key for the VM
[[ -f id_ed25519 ]] || ssh-keygen -q -t ed25519 -N '' -C nas-dev-vm -f id_ed25519

# 3. Disks: the system disk is a thin overlay on top of the base image,
#    so a reset costs nothing and the base image is never modified.
rm -f system.qcow2 disk[1-4].qcow2 seed.iso
qemu-img create -q -f qcow2 -F qcow2 -b "$IMG" system.qcow2 8G
for i in 1 2 3 4; do qemu-img create -q -f qcow2 "disk$i.qcow2" "$DATA_DISK_SIZE"; done

# 4. cloud-init: user, key and the packages the NAS needs
cat > user-data <<EOF
#cloud-config
hostname: nas-dev
users:
  - name: nas
    groups: [sudo]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys:
      - $(cat id_ed25519.pub)
package_update: true
packages:
  - mdadm          # software RAID
  - smartmontools  # disk health (smartctl)
  - btrfs-progs
  - samba          # SMB shares
  - wsdd2          # makes the NAS visible in Windows Explorer
  - avahi-daemon   # makes the NAS visible in macOS Finder
  - parted
  - nvme-cli
  - rsync          # backups
  - exfatprogs     # USB disks formatted exFAT
  - dosfstools     # USB disks formatted FAT32
  - hdparm         # disk spin-down
  - nut-server     # UPS support (Network UPS Tools)
  - nut-client
  - quota          # disk quotas (setquota, repquota)
  # The cloud kernel has no USB storage drivers: use Debian's standard
  # kernel, the same one the real ReadyNAS will run.
  - linux-image-amd64
runcmd:
  - [sh, -c, "DEBIAN_FRONTEND=noninteractive apt-get purge -y 'linux-image-*cloud*' && reboot"]
EOF
printf 'instance-id: nas-dev-%s\nlocal-hostname: nas-dev\n' "$(date +%s)" > meta-data
genisoimage -quiet -output seed.iso -volid cidata -joliet -rock user-data meta-data
rm -f user-data meta-data

rm -f usb*.qcow2
echo ">> VM ready. Start it with: vm/run.sh   (first boot installs packages and reboots, ~3 min)"
