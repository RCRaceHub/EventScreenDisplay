#!/bin/bash
# Build the eventscreen .deb for one architecture into dist/.
#
#   packaging/build-deb.sh [VERSION]
#   ARCH=armhf packaging/build-deb.sh 0.7.1
#
# ARCH is a Debian architecture: arm64 (64-bit Pi OS), armhf (32-bit Pi OS)
# or amd64. It defaults to this machine's. VERSION defaults to the one in
# internal/version/version.go; when given (CI passes the tag's version) it
# is stamped into the binary, so the agent reports the version it was
# released as. Needs Go and dpkg-deb, so it runs on any Debian/Ubuntu
# machine or CI runner; the binary is cross-compiled and fully static.
# Set BIN to package an already built binary instead, and MAINTAINER to
# override the Maintainer field.
set -euo pipefail

SRC="$(cd "$(dirname "$0")/.." && pwd)"
VERSION=${1:-$(sed -n 's/^var Version = "\(.*\)"/\1/p' "$SRC/internal/version/version.go")}
ARCH=${ARCH:-$(dpkg --print-architecture)}
MAINTAINER=${MAINTAINER:-"RCRaceHub <10654646+oggthemiffed@users.noreply.github.com>"}
OUT="$SRC/dist"
PKG="eventscreen_${VERSION}_${ARCH}"
ROOT="$(mktemp -d)/$PKG"
trap 'rm -rf "$(dirname "$ROOT")"' EXIT

case "$ARCH" in
  arm64) GOARCH=arm64 GOARM="" ;;
  # Raspberry Pi OS's 32-bit userland is ARMv6 hard-float, which runs on
  # every Pi (and GOARM=6 binaries run fine on the Pi 3 and Zero 2 W too).
  armhf) GOARCH=arm GOARM=6 ;;
  amd64) GOARCH=amd64 GOARM="" ;;
  *) echo "unsupported ARCH $ARCH (arm64, armhf or amd64)" >&2; exit 1 ;;
esac

# The binary.
install -d "$ROOT/usr/bin"
if [[ -n ${BIN:-} ]]; then
  install -m 0755 "$BIN" "$ROOT/usr/bin/eventscreen"
else
  (cd "$SRC" && CGO_ENABLED=0 GOOS=linux GOARCH=$GOARCH GOARM=$GOARM go build -trimpath \
    -ldflags "-s -w -X github.com/RCRaceHub/EventScreenDisplay/internal/version.Version=$VERSION" \
    -o "$ROOT/usr/bin/eventscreen" ./cmd/eventscreen)
fi

# Everything else, in its installed location.
install -D -m 0755 "$SRC/deploy/eventscreen-setup"   "$ROOT/usr/sbin/eventscreen-setup"
install -D -m 0755 "$SRC/deploy/eventscreen-check"   "$ROOT/usr/sbin/eventscreen-check"
install -D -m 0755 "$SRC/deploy/eventscreen-bootconfig" "$ROOT/usr/sbin/eventscreen-bootconfig"
install -D -m 0644 "$SRC/deploy/eventscreen-bootconfig.service" "$ROOT/usr/lib/systemd/system/eventscreen-bootconfig.service"
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
sed -e "s|@VERSION@|$VERSION|" -e "s|@ARCH@|$ARCH|" -e "s|@MAINTAINER@|$MAINTAINER|" -e "s|@SIZE@|$SIZE|" \
  "$SRC/packaging/debian/control" > "$ROOT/DEBIAN/control"
for f in postinst prerm postrm; do
  install -m 0755 "$SRC/packaging/debian/$f" "$ROOT/DEBIAN/$f"
done
# Files under /etc that apt must never overwrite once the user has edited them.
(cd "$ROOT" && find etc -type f | sed 's|^|/|' | sort) > "$ROOT/DEBIAN/conffiles"

mkdir -p "$OUT"
dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$OUT/$PKG.deb"
echo "built $OUT/$PKG.deb"
