#!/bin/bash
# Install the EventScreenDisplay viewer from a git checkout on Raspberry Pi OS
# Lite. Builds the eventscreen .deb for this Pi (needs Go: sudo apt install
# golang-go, or build it on another machine with "make deb-arm64"), installs
# it with apt, then runs eventscreen-setup with any options given:
#
#   sudo ./deploy/install.sh [--hostname screen-01] [--browser cog|chromium]
#                            [--name "Pit lane board"] [--port 8080] [--token SECRET]
#                            [--resolution 1920x1080@60D|auto]
#
# Safe to re-run: it upgrades the package and keeps your existing config. If
# you have a released .deb, "sudo apt install ./eventscreen_*.deb" followed by
# "sudo eventscreen-setup" does the same thing.
set -euo pipefail

if [[ ${1:-} == -h || ${1:-} == --help ]]; then
  sed -n '2,13p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi
if [[ $EUID -ne 0 ]]; then
  echo "run me with sudo" >&2
  exit 1
fi

SRC="$(cd "$(dirname "$0")/.." && pwd)"

# Clean up a pre-package install (0.1.0 copied files into /opt), so its unit
# in /etc/systemd/system doesn't shadow the packaged one.
if [[ -d /opt/eventscreen && ! -e /usr/lib/eventscreen ]]; then
  echo "==> Removing the old /opt/eventscreen install (config and playlist are kept)"
  systemctl disable --now eventscreen >/dev/null 2>&1 || true
  rm -rf /opt/eventscreen /etc/systemd/system/eventscreen.service \
    /etc/systemd/system.conf.d/eventscreen-watchdog.conf
  systemctl daemon-reload
fi

ARCH=$(dpkg --print-architecture)
if ! command -v go >/dev/null; then
  echo "Go isn't installed, so the package can't be built here. Either" >&2
  echo "  sudo apt install golang-go   and run this again, or" >&2
  echo "  download eventscreen_<version>_${ARCH}.deb from the GitHub releases and" >&2
  echo "  sudo apt install ./eventscreen_*_${ARCH}.deb && sudo eventscreen-setup" >&2
  exit 1
fi
echo "==> Building the package for $ARCH"
ARCH=$ARCH "$SRC/packaging/build-deb.sh"
DEB=$(ls -t "$SRC"/dist/eventscreen_*_"$ARCH".deb | head -n1)

echo "==> Installing $DEB"
apt-get update
# confold: keep an existing /etc/eventscreen/config.toml without asking.
apt-get install -y -o Dpkg::Options::=--force-confold "$DEB"

eventscreen-setup "$@"
