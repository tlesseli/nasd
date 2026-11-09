#!/usr/bin/env bash
# Builds a ready-to-boot system image for the ReadyNAS, on this PC:
#   Debian 13 + NAS packages + nasd, standard kernel (USB drivers),
#   DHCP on any network card, SSH with a dedicated key, hostname "nas".
#
# Steps: start from Debian's cloud image, let cloud-init provision it once in
# QEMU (it powers off when done), then produce image/nas.raw to write on a
# USB SSD or stick (image/write.sh).
#
# Usage: image/build.sh          your own NAS: your SSH key and a console
#                                password (image/credentials.txt) are built in
#        image/build.sh --dist   an image to give to others (nas-dist.raw):
#                                no key, no password (the account is locked);
#                                the owner adds their SSH key in the web UI.
# Either way, every installation generates its own SSH host keys at first
# boot and starts without nasd data (no shared secrets between copies).
# (needs: qemu, genisoimage, internet)
set -euo pipefail
DIST=0; OUT=nas.raw
if [[ ${1:-} == --dist ]]; then DIST=1; OUT=nas-dist.raw; fi
cd "$(dirname "$0")"
ROOT=$(cd .. && pwd)
mkdir -p build

BASE=../vm/debian-13-genericcloud-amd64.qcow2   # downloaded by vm/setup.sh
SIZE=8G                                          # grows to the whole disk at first boot
[[ -f $BASE ]] || { echo "run vm/setup.sh first (it downloads and verifies the Debian image)"; exit 1; }

echo ">> building nasd"
(cd "$ROOT" && make build >/dev/null)
cp "$ROOT/bin/nasd" "$ROOT/deploy/nasd.service" build/

if (( DIST )); then
  # Nothing personal in an image for others.
  ACCOUNT="    lock_passwd: true"
else
# Dedicated SSH key for the NAS (kept here, never committed).
[[ -f id_ed25519 ]] || ssh-keygen -q -t ed25519 -N '' -C nas-admin -f id_ed25519
# A console password for the "nas" user: only for the serial console (SSH
# accepts keys only). Kept in image/credentials.txt.
if [[ ! -f credentials.txt ]]; then
  PW=$(head -c 12 /dev/urandom | base64 | tr -d '/+=' | head -c 16)
  printf 'user: nas\nconsole password: %s\nssh: ssh -i image/id_ed25519 nas@nas.local\n' "$PW" > credentials.txt
  chmod 600 credentials.txt
fi
PW=$(sed -n 's/^console password: //p' credentials.txt)
ACCOUNT="    lock_passwd: false
    plain_text_passwd: \"$PW\"
    ssh_authorized_keys:
      - $(cat id_ed25519.pub)"
fi

cat > build/user-data <<EOF
#cloud-config
hostname: nas
fqdn: nas
preserve_hostname: false
users:
  - name: nas
    groups: [sudo]
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
$ACCOUNT
ssh_pwauth: false
package_update: true
package_upgrade: true
packages:
  - linux-image-amd64   # standard kernel: USB storage, sensors, every NIC driver
  - mdadm
  - smartmontools
  - btrfs-progs
  - samba
  - wsdd2
  - avahi-daemon
  - parted
  - lvm2                # volumes made by RAIDiator: RAID → LVM → ext4
  - rsync
  - exfatprogs
  - dosfstools
  - hdparm
  - nut-server
  - nut-client
  - quota
  - vsftpd
  - rclone
  - ethtool
  - lm-sensors          # sensors-detect, to find the board's sensor chip
  - cloud-guest-utils   # growpart
write_files:
  # DHCP on whatever network card the NAS has (the cloud image only knew the VM's).
  - path: /etc/systemd/network/80-nas-dhcp.network
    content: |
      [Match]
      Name=en* eth*
      [Network]
      DHCP=yes
  # The ReadyNAS Ultra's sensor chip (IT8721F: fan speed and control).
  - path: /etc/modules-load.d/nas.conf
    content: |
      it87
  # Two network ports, often only one plugged in: online as soon as one is.
  - path: /etc/systemd/system/systemd-networkd-wait-online.service.d/any.conf
    content: |
      [Service]
      ExecStart=
      ExecStart=/usr/lib/systemd/systemd-networkd-wait-online --any --timeout=60
  # First boot on the real disk: grow the system partition to the whole disk.
  - path: /usr/local/sbin/nas-firstboot
    permissions: '0755'
    content: |
      #!/bin/sh
      set -e
      # This installation's own SSH identity (the image has none).
      rm -f /etc/ssh/ssh_host_*
      ssh-keygen -A
      ROOT=\$(findmnt -no SOURCE /)
      DISK=/dev/\$(lsblk -no PKNAME "\$ROOT")
      PART=\$(cat /sys/class/block/\$(basename "\$ROOT")/partition)
      growpart "\$DISK" "\$PART" || true
      resize2fs "\$ROOT"
      # On the ReadyNAS, Netgear's boot loader on the internal flash starts
      # our kernel: mount that flash on /boot/flash so updates reach it.
      # It is recognised by Netgear's "vpd" file next to our kernel.
      mkdir -p /mnt/probe /boot/flash
      for DEV in \$(blkid -t TYPE=vfat -o device); do
        mount -o ro "\$DEV" /mnt/probe 2>/dev/null || continue
        if [ -f /mnt/probe/vpd ] && [ -f /mnt/probe/nas.krn ] && ! grep -q /boot/flash /etc/fstab; then
          echo "UUID=\$(blkid -s UUID -o value "\$DEV") /boot/flash vfat ro,nofail 0 0" >> /etc/fstab
        fi
        umount /mnt/probe
      done
      systemctl daemon-reload
      mount /boot/flash 2>/dev/null && /usr/local/sbin/nas-flash-sync || true
      touch /var/lib/nas-firstboot.done
  # Copies the newest kernel and initramfs to the ReadyNAS flash (nas.krn,
  # nas.ird). Called after kernel and initramfs updates; no-op elsewhere.
  - path: /usr/local/sbin/nas-flash-sync
    permissions: '0755'
    content: |
      #!/bin/sh
      # The flash stays mounted read-only; writable only while copying, so a
      # power cut can never leave it half-written.
      FLASH=/boot/flash
      mountpoint -q \$FLASH || exit 0
      K=\$(ls -1 /boot/vmlinuz-* 2>/dev/null | sort -V | tail -1)
      V=\${K#/boot/vmlinuz-}
      I=/boot/initrd.img-\$V
      [ -f "\$K" ] && [ -f "\$I" ] || exit 0
      cmp -s "\$K" \$FLASH/nas.krn && cmp -s "\$I" \$FLASH/nas.ird && exit 0
      mount -o remount,rw \$FLASH || exit 1
      ok=0
      cp "\$K" \$FLASH/nas.krn && cp "\$I" \$FLASH/nas.ird && ok=1
      sync
      mount -o remount,ro \$FLASH
      [ \$ok = 1 ] || exit 1
      echo "nas-flash-sync: the flash boots \$V"
  - path: /etc/kernel/postinst.d/zz-nas-flash
    permissions: '0755'
    content: |
      #!/bin/sh
      /usr/local/sbin/nas-flash-sync || echo "WARNING: could not update the boot flash" >&2
      exit 0
  - path: /etc/kernel/postrm.d/zz-nas-flash
    permissions: '0755'
    content: |
      #!/bin/sh
      /usr/local/sbin/nas-flash-sync || echo "WARNING: could not update the boot flash" >&2
      exit 0
  - path: /etc/initramfs/post-update.d/nas-flash
    permissions: '0755'
    content: |
      #!/bin/sh
      /usr/local/sbin/nas-flash-sync || echo "WARNING: could not update the boot flash" >&2
      exit 0
  - path: /etc/systemd/system/nas-firstboot.service
    content: |
      [Unit]
      Description=First boot: own SSH host keys, grow the system partition
      ConditionPathExists=!/var/lib/nas-firstboot.done
      Before=ssh.service sshd.service
      [Service]
      Type=oneshot
      ExecStart=/usr/local/sbin/nas-firstboot
      [Install]
      WantedBy=multi-user.target
runcmd:
  # nasd and its service, from the seed disk.
  - [sh, -c, "mkdir -p /mnt/seed && mount -L cidata /mnt/seed && install -m 755 /mnt/seed/nasd /usr/local/bin/nasd && install -m 644 /mnt/seed/nasd.service /etc/systemd/system/nasd.service && umount /mnt/seed"]
  - [systemctl, enable, nasd, nas-firstboot]
  # Services nasd switches on itself when you enable them in the UI.
  - [systemctl, disable, --now, vsftpd, rsync, nut-server, nut-monitor]
  # Network: drop the VM-specific config generated by cloud-init.
  - [sh, -c, "rm -f /etc/netplan/50-cloud-init.yaml /run/systemd/network/10-netplan-*; echo 'network: {config: disabled}' > /etc/cloud/cloud.cfg.d/99-disable-network.cfg"]
  # Standard kernel only, and a boot image with all storage drivers.
  - [sh, -c, "DEBIAN_FRONTEND=noninteractive apt-get purge -y 'linux-image-*cloud*' && sed -i 's/^MODULES=.*/MODULES=most/' /etc/initramfs-tools/initramfs.conf && update-initramfs -u -k all && update-grub"]
  # cloud-init has done its job: on the NAS there is no cloud to ask.
  - [touch, /etc/cloud/cloud-init.disabled]
  # No secret may be shared between copies of the image: SSH host keys are
  # made at first boot, nasd data (TLS key, state) at first start.
  - [sh, -c, "apt-get clean; rm -f /var/lib/nas-firstboot.done /etc/ssh/ssh_host_*; rm -rf /var/lib/nasd; truncate -s0 /etc/machine-id"]
  - [poweroff]
EOF
printf 'instance-id: nas-image-%s\nlocal-hostname: nas\n' "$(date +%s)" > build/meta-data
genisoimage -quiet -output build/seed.iso -volid cidata -joliet -rock \
  build/user-data build/meta-data build/nasd build/nasd.service

echo ">> provisioning in QEMU (5-10 min: downloads and installs packages, then powers off)"
rm -f build/nas.qcow2
qemu-img create -q -f qcow2 -F qcow2 -b "$(cd ../vm && pwd)/$(basename "$BASE")" build/nas.qcow2 "$SIZE"
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -smp 2 -m 1536 -display none -serial file:build/provision.log \
  -drive file=build/nas.qcow2,if=virtio,format=qcow2 -drive file=build/seed.iso,if=virtio,format=raw,readonly=on \
  -nic user,model=virtio-net-pci

grep -q 'linux-image-amd64' build/provision.log || true
echo ">> converting to a raw disk image"
qemu-img convert -O raw build/nas.qcow2 "$OUT"
ls -lh "$OUT"
./flash-files.sh "$OUT"
echo ">> done. Test it with image/test.sh, then write it with image/write.sh"
