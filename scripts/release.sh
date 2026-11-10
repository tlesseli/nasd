#!/usr/bin/env bash
# Builds a release to publish, in release/v<version>/:
#   nasd-<v>-readynas-ultra4.img.xz   the system image (--dist: no key, no
#                                     password, no host keys: nothing personal)
#   flash/                            the three files for the ReadyNAS flash
#   nasd-<v>-source.tar.gz            the source of this exact version (GPL)
#   debian-packages.txt               every Debian package in the image, with
#                                     its version (their source: SOURCE-OFFER.txt)
#   SOURCE-OFFER.txt                  how to get all corresponding source
#   SHA256SUMS                        checksums of everything above
#
# Usage: scripts/release.sh   (from a clean, tagged commit: git tag -a v1.7.2)
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
TAG=v$VERSION

[[ -z $(git status --porcelain) ]] || { echo "commit or stash your changes first (the release must match a commit)"; exit 1; }
[[ $(git describe --exact-match --tags 2>/dev/null) == "$TAG" ]] || { echo "HEAD is not tagged $TAG: git tag -a $TAG -m 'nasd $VERSION'"; exit 1; }

OUT=release/$TAG
rm -rf "$OUT" && mkdir -p "$OUT/flash"

echo ">> building the distributable image (~10 min)"
image/build.sh --dist

# Nothing personal may be in it: check before publishing.
echo ">> checking the image holds no secrets"
START=$(sfdisk --json image/nas-dist.raw | python3 -c 'import json,sys
for p in json.load(sys.stdin)["partitiontable"]["partitions"]:
    if p["type"] == "4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709": print(p["start"])')
FS="image/nas-dist.raw?offset=$((START * 512))"
ls_in() { debugfs -R "ls -l $1" "$FS" 2>/dev/null; }
fail=0
ls_in /home/nas/.ssh | grep -q authorized_keys && { echo "!! an authorized_keys file is in the image"; fail=1; }
ls_in /etc/ssh | grep -q 'ssh_host_.*key' && { echo "!! SSH host keys are in the image"; fail=1; }
ls_in /var/lib | grep -qw nasd && { echo "!! nasd data (/var/lib/nasd) is in the image"; fail=1; }
debugfs -R "cat /etc/shadow" "$FS" 2>/dev/null | awk -F: '$1=="nas" && $2 !~ /^[!*]/ {exit 1}' || { echo "!! the nas account has a password"; fail=1; }
(( fail == 0 )) || { echo "release aborted"; exit 1; }
echo "   no key, no host keys, no nasd data, nas account locked"

echo ">> packages and source offer"
debugfs -R "cat /var/lib/dpkg/status" "$FS" 2>/dev/null | awk '
  /^Package: / {p=$2} /^Status: / {ok = ($0 ~ /installed$/)} /^Version: / {if (ok) print p" "$2}' | sort > "$OUT/debian-packages.txt"
cat > "$OUT/SOURCE-OFFER.txt" <<EOF
nasd $VERSION: corresponding source code

nasd itself (GPL-3.0): nasd-$VERSION-source.tar.gz in this release, also at
the tag $TAG of the project's repository.

The image is a Debian 13 system. Each package listed in debian-packages.txt
keeps its own license (see /usr/share/doc/<package>/copyright in the image).
Their exact source code is available from Debian:
  https://snapshot.debian.org/  (any version, by package name)
  apt-get source <package>=<version>  (on a Debian 13 machine)
On request, for three years from this release, we will also provide that
source on a physical medium for no more than the cost of making and sending
it: tanguy@ulixes-consulting.be
EOF

echo ">> source archive"
git archive --format=tar.gz --prefix="nasd-$VERSION/" -o "$OUT/nasd-$VERSION-source.tar.gz" "$TAG"

echo ">> flash files"
cp image/build/flash/nas.krn image/build/flash/nas.ird image/build/flash/syslinux.cfg "$OUT/flash/"

echo ">> compressing the image (xz)"
xz -T0 -6 -c image/nas-dist.raw > "$OUT/nasd-$VERSION-readynas-ultra4.img.xz"

(cd "$OUT" && find . -type f ! -name SHA256SUMS -printf '%P\n' | sort | xargs sha256sum > SHA256SUMS)
echo ">> done: $OUT"
ls -lh "$OUT" "$OUT/flash"
