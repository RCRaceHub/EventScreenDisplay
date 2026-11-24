#!/bin/bash
# Build the EventScreen SD card image: the official Raspberry Pi OS Lite
# (64-bit) image with the eventscreen package and its browser already
# installed and the display set up for a kiosk.
#
#   sudo image/build-image.sh BASE.img.xz eventscreen_X_arm64.deb OUT.img
#
# BASE is the Raspberry Pi OS Lite arm64 image, as downloaded (see
# image/base-image.env for the one releases use). OUT is the uncompressed
# image; compress it with xz afterwards. Runs on Linux as root, with loop
# devices, mtools and qemu-user binfmt for aarch64 registered with the F flag
# (Debian/Ubuntu: apt install qemu-user-static mtools), because the package is
# installed in a chroot of the image's own root filesystem. It needs the
# internet to download the browser and its libraries from the Pi OS repos.
#
# Per-card settings are left to Raspberry Pi Imager (hostname, user, Wi-Fi,
# SSH) and to eventscreen.txt on the boot partition (name, API token, port,
# resolution); see docs/sd-card-image.md.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
fi
BASE=$1
DEB=$(realpath "$2")
OUT=$3
SRC="$(cd "$(dirname "$0")/.." && pwd)"
# Room for the browser and its libraries. Pi OS grows the root partition to
# fill the SD card on first boot, so this only needs to fit the install.
GROW=${GROW:-1200M}

[[ $EUID -eq 0 ]] || { echo "run me as root" >&2; exit 1; }
[[ -f $DEB ]] || { echo "no such package: $DEB" >&2; exit 1; }

MNT=$(mktemp -d)
LOOP=""
cleanup() {
  set +e
  for m in dev/pts dev proc sys ""; do
    mountpoint -q "$MNT/$m" && umount "$MNT/$m"
  done
  [[ -n $LOOP ]] && losetup -d "$LOOP"
  rmdir "$MNT"
}
trap cleanup EXIT

echo "==> Unpacking $BASE"
xz -dc "$BASE" > "$OUT"
truncate -s +"$GROW" "$OUT"
parted -s "$OUT" resizepart 2 100%

# The root partition gets a loop device by offset (losetup -P can't create
# partition devices in some containers). The FAT boot partition is edited
# with mtools, so no FAT driver is needed either.
read -r BOOT_START < <(partx -g -o START -n 1 "$OUT")
BOOT_FAT="$OUT@@$((BOOT_START * 512))"
read -r ROOT_START ROOT_SECTORS < <(partx -g -o START,SECTORS -n 2 "$OUT")
LOOP=$(losetup --show -f -o $((ROOT_START * 512)) --sizelimit $((ROOT_SECTORS * 512)) "$OUT")
ROOT_LOOP=$LOOP
e2fsck -fy "$ROOT_LOOP" >/dev/null || [[ $? -le 1 ]]
resize2fs "$ROOT_LOOP" >/dev/null

mount "$ROOT_LOOP" "$MNT"
# The chroot sees only the boot partition's cmdline.txt, which
# eventscreen-setup edits; it's copied back afterwards.
install -d "$MNT/boot/firmware"
if [[ -n $(ls -A "$MNT/boot/firmware") ]]; then
  echo "the image's /boot/firmware isn't empty" >&2; exit 1
fi
mcopy -n -i "$BOOT_FAT" ::cmdline.txt "$MNT/boot/firmware/cmdline.txt"
mount --bind /dev "$MNT/dev"
mount --bind /dev/pts "$MNT/dev/pts"
mount -t proc proc "$MNT/proc"
mount -t sysfs sys "$MNT/sys"

# Name resolution for apt, and no services started inside the chroot.
RESOLV_BACKUP=""
if [[ -e $MNT/etc/resolv.conf || -L $MNT/etc/resolv.conf ]]; then
  RESOLV_BACKUP=$(mktemp)
  cp -P "$MNT/etc/resolv.conf" "$RESOLV_BACKUP.link"
  rm -f "$MNT/etc/resolv.conf"
fi
cp /etc/resolv.conf "$MNT/etc/resolv.conf"
printf '#!/bin/sh\nexit 101\n' > "$MNT/usr/sbin/policy-rc.d"
chmod 0755 "$MNT/usr/sbin/policy-rc.d"

echo "==> Installing $(basename "$DEB")"
cp "$DEB" "$MNT/tmp/eventscreen.deb"
chroot "$MNT" /bin/bash -euo pipefail <<'CHROOT'
export DEBIAN_FRONTEND=noninteractive LC_ALL=C
apt-get update -q
apt-get install -y -q /tmp/eventscreen.deb
# Recommends, named so a change to them can't silently drop the browser.
apt-get install -y -q cog libgles2 libnss-mdns fonts-dejavu-core
apt-get clean
rm -rf /var/lib/apt/lists/* /tmp/eventscreen.deb
# Display settings (1080p forced on HDMI, no console blanking, no login
# prompt on the TV), as eventscreen-setup does on a hand-installed Pi.
eventscreen-setup --offline
# Pi OS turns the tty1 login prompt on during first boot, after the setup
# above has looked, so mask it for good. SSH still works, and Alt+F2 on a
# keyboard still gives a login prompt.
systemctl mask getty@tty1.service
eventscreen version
CHROOT

# Nothing else may have written to the boot partition: it would be lost.
extra=$(find "$MNT/boot/firmware" -mindepth 1 ! -path "$MNT/boot/firmware/cmdline.txt" -printf '%P ')
if [[ -n $extra ]]; then
  echo "the install wrote to the boot partition, which this script doesn't copy back: $extra" >&2
  exit 1
fi
echo "==> Writing the boot partition"
echo "cmdline.txt: $(cat "$MNT/boot/firmware/cmdline.txt")"
mcopy -o -i "$BOOT_FAT" "$MNT/boot/firmware/cmdline.txt" ::cmdline.txt
rm "$MNT/boot/firmware/cmdline.txt"
mcopy -o -i "$BOOT_FAT" "$SRC/image/eventscreen.txt.example" ::eventscreen.txt.example
VERSION=$(chroot "$MNT" eventscreen version)
{
  echo "EVENTSCREEN_VERSION=$VERSION"
  echo "BASE_IMAGE=$(basename "$BASE")"
} > "$MNT/etc/eventscreen-image"

rm -f "$MNT/usr/sbin/policy-rc.d" "$MNT/etc/resolv.conf"
if [[ -n $RESOLV_BACKUP ]]; then
  cp -P "$RESOLV_BACKUP.link" "$MNT/etc/resolv.conf"
  rm -f "$RESOLV_BACKUP" "$RESOLV_BACKUP.link"
fi
# Fill free space with zeros so the image compresses well.
fstrim "$MNT" 2>/dev/null || true

echo "==> Built $OUT with eventscreen $VERSION"
