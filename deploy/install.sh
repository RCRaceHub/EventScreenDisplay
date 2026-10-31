#!/bin/bash
# Install the EventScreenDisplay viewer agent on Raspberry Pi OS Lite (Bookworm).
#
#   sudo ./deploy/install.sh [--hostname screen-01] [--browser cog|chromium]
#                            [--name "Pit lane board"] [--port 8080] [--token SECRET]
#
# Safe to re-run: it upgrades the code and keeps your existing config.
set -euo pipefail

HOSTNAME_NEW=""
BROWSER=""
NAME=""
PORT=""
TOKEN=""

usage() { sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --hostname) HOSTNAME_NEW="$2"; shift 2 ;;
    --browser)  BROWSER="$2"; shift 2 ;;
    --name)     NAME="$2"; shift 2 ;;
    --port)     PORT="$2"; shift 2 ;;
    --token)    TOKEN="$2"; shift 2 ;;
    -h|--help)  usage 0 ;;
    *) echo "unknown option: $1" >&2; usage 1 ;;
  esac
done

if [[ $EUID -ne 0 ]]; then
  echo "run me with sudo" >&2
  exit 1
fi

SRC="$(cd "$(dirname "$0")/.." && pwd)"
APP=/opt/eventscreen
ETC=/etc/eventscreen
CONF=$ETC/config.toml

step() { echo; echo "==> $*"; }

# An existing config decides the browser unless one was given on the command line.
if [[ -z "$BROWSER" && -f $CONF ]]; then
  BROWSER=$(sed -n 's/^browser *= *"\(.*\)".*/\1/p' "$CONF" | head -n1)
fi
BROWSER=${BROWSER:-cog}

step "Installing packages for browser=$BROWSER"
PKGS=(python3 dbus avahi-daemon libnss-mdns)
case "$BROWSER" in
  cog)      PKGS+=(cog) ;;
  chromium) PKGS+=(cage seatd chromium-browser) ;;
  dummy)    ;;
  *) echo "unknown browser: $BROWSER" >&2; exit 1 ;;
esac
apt-get update
apt-get install -y --no-install-recommends "${PKGS[@]}" fonts-dejavu-core
if [[ $BROWSER == chromium ]]; then
  systemctl enable --now seatd
fi

if [[ -n "$HOSTNAME_NEW" ]]; then
  step "Setting hostname to $HOSTNAME_NEW (reachable as $HOSTNAME_NEW.local)"
  hostnamectl set-hostname "$HOSTNAME_NEW"
  if grep -q '^127\.0\.1\.1' /etc/hosts; then
    sed -i "s/^127\.0\.1\.1.*/127.0.1.1\t$HOSTNAME_NEW/" /etc/hosts
  else
    printf '127.0.1.1\t%s\n' "$HOSTNAME_NEW" >> /etc/hosts
  fi
fi

step "Creating the eventscreen user"
if ! id eventscreen >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/eventscreen --shell /usr/sbin/nologin eventscreen
fi
for g in video render input _seatd; do
  if getent group "$g" >/dev/null; then usermod -aG "$g" eventscreen; fi
done
install -d -o eventscreen -g eventscreen /var/lib/eventscreen

step "Installing the agent to $APP"
install -d "$APP"
rm -rf "$APP/eventscreen"
cp -r "$SRC/eventscreen" "$APP/"
find "$APP" -name __pycache__ -prune -exec rm -rf {} +

step "Writing config"
install -d "$ETC"
if [[ ! -f $CONF ]]; then
  install -m 0640 -g eventscreen "$SRC/deploy/config.example.toml" "$CONF"
fi
set_conf() {  # set_conf key 'toml value'
  if grep -qE "^#? *$1 *=" "$CONF"; then
    sed -i -E "s|^#? *$1 *=.*|$1 = $2|" "$CONF"
  else
    echo "$1 = $2" >> "$CONF"
  fi
}
set_conf browser "\"$BROWSER\""
[[ -n "$NAME"  ]] && set_conf name "\"$NAME\""
[[ -n "$PORT"  ]] && set_conf port "$PORT"
[[ -n "$TOKEN" ]] && set_conf api_token "\"$TOKEN\""
PORT=$(sed -n 's/^port *= *\([0-9]*\).*/\1/p' "$CONF" | head -n1)
PORT=${PORT:-8080}
SCREEN_ID=$(sed -n 's/^screen_id *= *"\(.*\)".*/\1/p' "$CONF" | head -n1)
SCREEN_ID=${SCREEN_ID:-$(hostname)}
VERSION=$(sed -n 's/^__version__ = "\(.*\)"/\1/p' "$APP/eventscreen/__init__.py")

step "Advertising the screen over mDNS"
sed -e "s|@PORT@|$PORT|g" -e "s|@ID@|$SCREEN_ID|g" -e "s|@VERSION@|$VERSION|g" \
  "$SRC/deploy/eventscreen-avahi.service" > /etc/avahi/services/eventscreen.service
systemctl enable avahi-daemon
systemctl restart avahi-daemon

step "Tuning the console for a kiosk"
# Stop the text console blanking the screen and keep the login prompt off it.
CMDLINE=/boot/firmware/cmdline.txt
[[ -f $CMDLINE ]] || CMDLINE=/boot/cmdline.txt
if [[ -f $CMDLINE ]] && ! grep -q consoleblank= "$CMDLINE"; then
  sed -i '1 s/$/ consoleblank=0/' "$CMDLINE"
fi
systemctl disable getty@tty1.service >/dev/null 2>&1 || true

step "Installing and starting the service"
install -m 0644 "$SRC/deploy/eventscreen.service" /etc/systemd/system/eventscreen.service
systemctl daemon-reload
systemctl enable eventscreen
systemctl restart eventscreen

cat <<EOF

Done. The screen should show its idle page within a few seconds.

  API:     http://$(hostname).local:$PORT/api/v1/status
  Config:  $CONF
  Logs:    journalctl -u eventscreen -f

A reboot is recommended after the first install.
EOF
