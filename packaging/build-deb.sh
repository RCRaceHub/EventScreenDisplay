#!/bin/bash
# Build the eventscreen .deb into dist/.
#
#   packaging/build-deb.sh [VERSION]
#
# VERSION defaults to __version__ in eventscreen/__init__.py. Only needs
# dpkg-deb, so it runs on any Debian/Ubuntu machine, a Pi or a CI runner.
# Set MAINTAINER to override the Maintainer field.
set -euo pipefail

SRC="$(cd "$(dirname "$0")/.." && pwd)"
VERSION=${1:-$(sed -n 's/^__version__ = "\(.*\)"/\1/p' "$SRC/eventscreen/__init__.py")}
MAINTAINER=${MAINTAINER:-"RCRaceHub <10654646+oggthemiffed@users.noreply.github.com>"}
OUT="$SRC/dist"
PKG="eventscreen_${VERSION}_all"
ROOT="$(mktemp -d)/$PKG"
trap 'rm -rf "$(dirname "$ROOT")"' EXIT

# Files, in their installed locations.
install -d "$ROOT/usr/lib/eventscreen"
cp -r "$SRC/eventscreen" "$ROOT/usr/lib/eventscreen/"
find "$ROOT/usr/lib/eventscreen" -name __pycache__ -prune -exec rm -rf {} +
find "$ROOT/usr/lib/eventscreen" -type f -exec chmod 0644 {} +
install -D -m 0755 "$SRC/deploy/eventscreen-setup"   "$ROOT/usr/sbin/eventscreen-setup"
install -D -m 0644 "$SRC/deploy/eventscreen.service" "$ROOT/usr/lib/systemd/system/eventscreen.service"
install -D -m 0644 "$SRC/deploy/eventscreen-wifi.service" "$ROOT/usr/lib/systemd/system/eventscreen-wifi.service"
install -D -m 0644 "$SRC/deploy/watchdog.conf"       "$ROOT/usr/lib/systemd/system.conf.d/eventscreen-watchdog.conf"
install -D -m 0644 "$SRC/deploy/config.example.toml" "$ROOT/etc/eventscreen/config.toml"
install -D -m 0644 "$SRC/deploy/wifi-powersave.conf" "$ROOT/etc/NetworkManager/conf.d/eventscreen-wifi-powersave.conf"
install -D -m 0644 "$SRC/deploy/captive-dns.conf"    "$ROOT/etc/NetworkManager/dnsmasq-shared.d/eventscreen-captive.conf"
install -D -m 0644 "$SRC/README.md" "$ROOT/usr/share/doc/eventscreen/README.md"
cp "$SRC"/docs/*.md "$ROOT/usr/share/doc/eventscreen/"

# Package metadata.
install -d "$ROOT/DEBIAN"
SIZE=$(du -sk --exclude=DEBIAN "$ROOT" | cut -f1)
sed -e "s|@VERSION@|$VERSION|" -e "s|@MAINTAINER@|$MAINTAINER|" -e "s|@SIZE@|$SIZE|" \
  "$SRC/packaging/debian/control" > "$ROOT/DEBIAN/control"
for f in postinst prerm postrm; do
  install -m 0755 "$SRC/packaging/debian/$f" "$ROOT/DEBIAN/$f"
done
# Files under /etc that apt must never overwrite once the user has edited them.
(cd "$ROOT" && find etc -type f | sed 's|^|/|' | sort) > "$ROOT/DEBIAN/conffiles"

mkdir -p "$OUT"
dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$OUT/$PKG.deb"
echo "built $OUT/$PKG.deb"
