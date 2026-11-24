#!/bin/bash
# Check a built EventScreen SD card image without booting it: the viewer is
# installed with its browser, its services are enabled for first boot, the
# display is set up and nothing from the build was left behind.
#
#   sudo image/check-image.sh eventscreen.img
#
# Prints a line per check and exits 1 if any failed. Needs root, losetup
# and mtools, like build-image.sh.
set -uo pipefail

IMG=${1:?usage: check-image.sh IMAGE.img}
[[ $EUID -eq 0 ]] || { echo "run me as root" >&2; exit 1; }
MNT=$(mktemp -d)
LOOP=""
cleanup() {
  mountpoint -q "$MNT" && umount "$MNT"
  [[ -n $LOOP ]] && losetup -d "$LOOP"
  rmdir "$MNT"
}
trap cleanup EXIT

read -r BOOT_START < <(partx -g -o START -n 1 "$IMG")
BOOT_FAT="$IMG@@$((BOOT_START * 512))"
read -r ROOT_START ROOT_SECTORS < <(partx -g -o START,SECTORS -n 2 "$IMG")
LOOP=$(losetup --show -r -f -o $((ROOT_START * 512)) --sizelimit $((ROOT_SECTORS * 512)) "$IMG")
mount -r "$LOOP" "$MNT"

FAILED=0
check() {  # check "what" command...
  local what=$1; shift
  if "$@" >/dev/null 2>&1; then
    echo "  PASS  $what"
  else
    echo "  FAIL  $what"
    FAILED=1
  fi
}
enabled() { [[ -L $MNT/etc/systemd/system/multi-user.target.wants/$1 ]]; }
masked() { [[ $(readlink "$MNT/etc/systemd/system/$1") == /dev/null ]]; }
cmdline() { mtype -i "$BOOT_FAT" ::cmdline.txt | grep -q -- "$1"; }

echo "Checking $IMG"
check "eventscreen installed" test -x "$MNT/usr/bin/eventscreen"
check "eventscreen-setup installed" test -x "$MNT/usr/sbin/eventscreen-setup"
check "eventscreen-bootconfig installed" test -x "$MNT/usr/sbin/eventscreen-bootconfig"
check "cog installed" test -x "$MNT/usr/bin/cog"
check "libgles2 installed (Cog's renderer crashes without it)" compgen -G "$MNT/usr/lib/aarch64-linux-gnu/libGLESv2.so.2*"
check "config installed" test -f "$MNT/etc/eventscreen/config.toml"
check "eventscreen user" grep -q '^eventscreen:' "$MNT/etc/passwd"
check "eventscreen.service enabled" enabled eventscreen.service
check "eventscreen-wifi.service enabled" enabled eventscreen-wifi.service
check "eventscreen-bootconfig.service enabled" enabled eventscreen-bootconfig.service
check "avahi-daemon enabled" enabled avahi-daemon.service
check "NetworkManager enabled" enabled NetworkManager.service
check "no login prompt on the TV (getty@tty1 masked)" masked getty@tty1.service
check "HDMI forced to 1080p" cmdline 'video=HDMI-A-1:1920x1080@60D'
check "console blanking off" cmdline 'consoleblank=0'
check "cmdline.txt is one line" test "$(mtype -i "$BOOT_FAT" ::cmdline.txt | wc -l)" -le 1
check "Pi OS still grows the root partition on first boot" cmdline 'resize'
check "settings template on the boot partition" mtype -i "$BOOT_FAT" ::eventscreen.txt.example
check "no eventscreen.txt on the boot partition" bash -c "! mtype -i '$BOOT_FAT' ::eventscreen.txt"
check "image info" grep -q '^EVENTSCREEN_VERSION=' "$MNT/etc/eventscreen-image"
check "build's policy-rc.d removed" test ! -e "$MNT/usr/sbin/policy-rc.d"
check "build's package removed" test ! -e "$MNT/tmp/eventscreen.deb"
check "apt lists cleaned" bash -c "! compgen -G '$MNT/var/lib/apt/lists/*_Packages'"
check "nothing left in /boot/firmware on the root partition" bash -c "[[ -z \$(ls -A '$MNT/boot/firmware') ]]"

if [[ $FAILED -ne 0 ]]; then
  echo "Some checks failed."
  exit 1
fi
echo "All checks passed."
