# Installing on a Raspberry Pi

This sets up a Pi as a screen that boots straight into the viewer. Allow
about 20 minutes per Pi the first time, most of it writing the SD card and
downloading packages.

## What you need

- Raspberry Pi **Zero 2 W** or **Pi 3** (a Pi 4 or 5 works too).
- 8 GB or larger microSD card.
- A TV or monitor on HDMI (the Zero 2 W needs a mini-HDMI adapter).
- A good power supply. Under-powered Pis brown out and corrupt SD cards.
- Network: Wi-Fi or Ethernet on the same LAN as the timing system.

## 1. Write the SD card

1. Install [Raspberry Pi Imager](https://www.raspberrypi.com/software/).
2. Choose **Raspberry Pi OS Lite (64-bit)** (Bookworm). For a Pi Zero 2 W the
   64-bit Lite image is fine; no desktop is needed.
3. Open the settings (the cog, or Ctrl+Shift+X) and set:
   - **Hostname**: the screen's name, for example `screen-01`. Use a
     different name for every screen.
   - **Username and password** for yourself.
   - **Wi-Fi** network and country, if not using Ethernet.
   - **Enable SSH**.
4. Write the card, put it in the Pi, connect the screen and power it on.

## 2. Install the viewer

From your laptop, on the same network:

```bash
ssh <you>@screen-01.local
sudo apt-get update && sudo apt-get install -y git
git clone https://github.com/RCRaceHub/EventScreenDisplay.git
cd EventScreenDisplay
sudo ./deploy/install.sh --hostname screen-01 --name "Pit lane board"
sudo reboot
```

After the reboot the TV shows the idle screen with the screen's name and API
address. It's ready for a playlist; see the [user guide](user-guide.md).

### Installer options

| Option | Meaning |
| --- | --- |
| `--hostname NAME` | Sets the Pi's hostname, so it is reachable as `NAME.local`. |
| `--name "TEXT"` | Friendly name shown on the idle screen and in the API. |
| `--browser cog\|chromium` | Browser to use. Default `cog` (recommended). |
| `--port N` | API port. Default 8080. |
| `--token SECRET` | Require `Authorization: Bearer SECRET` for any change. |

### What the installer does

1. Installs `python3`, `dbus`, `avahi-daemon` and the browser (`cog`, or
   `cage` + `seatd` + `chromium-browser`).
2. Sets the hostname if asked.
3. Creates an `eventscreen` system user with access to the display.
4. Copies the agent to `/opt/eventscreen`.
5. Creates `/etc/eventscreen/config.toml` from `deploy/config.example.toml`
   (an existing config is kept; command-line options are written into it).
6. Installs the mDNS advertisement in `/etc/avahi/services/`.
7. Stops the console blanking (`consoleblank=0` in `cmdline.txt`) and takes
   the login prompt off the HDMI console (`getty@tty1` disabled; SSH still works).
8. Installs, enables and starts the `eventscreen` systemd service.

## Updating

```bash
cd ~/EventScreenDisplay
git pull
sudo ./deploy/install.sh
```

Your config and the current playlist are kept.

## Files and locations

| Path | What |
| --- | --- |
| `/opt/eventscreen/` | The agent code |
| `/etc/eventscreen/config.toml` | Configuration (see comments inside) |
| `/var/lib/eventscreen/playlist.json` | The saved playlist |
| `/etc/systemd/system/eventscreen.service` | The service |
| `/etc/avahi/services/eventscreen.service` | mDNS advertisement |

## Display tips

- **Resolution.** The Pi picks the TV's preferred mode. On a Pi Zero, a 4K
  TV is hard work; force 1080p by adding `video=HDMI-A-1:1920x1080@60` to the
  single line in `/boot/firmware/cmdline.txt`.
- **Rotation (portrait screens).** Add `video=HDMI-A-1:1920x1080@60,rotate=90`
  to the same line instead.
- **Text too small or large.** Add Cog's scale option in the config, for
  example `browser_args = ["--scale=1.5"]`, then restart the service.
- **Overscan (black border or cropped edges on old TVs).** Turn off overscan
  on the TV ("Just scan", "Screen fit" or "1:1").

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Screen stays on the text console | `journalctl -u eventscreen -e` for errors; `systemctl status eventscreen` |
| `screen-01.local` doesn't resolve | Use the IP (`hostname -I` on the Pi). Some Windows PCs and guest Wi-Fi block mDNS. `systemctl status avahi-daemon` |
| A page is blank or looks wrong in Cog | Try it with `--browser chromium` to see if it's a WebKit issue |
| Pi Zero reboots or freezes | Usually power. Check `vcgencmd get_throttled` (anything but `0x0` means under-voltage or throttling) |
| Page loads but stops updating | `POST /api/v1/control/reload`, and check the timing server is reachable from the Pi |

Live logs: `journalctl -u eventscreen -f`. For more detail, add `-v` to the
`ExecStart` line in the service and run `sudo systemctl daemon-reload &&
sudo systemctl restart eventscreen`.

## Uninstalling

```bash
sudo systemctl disable --now eventscreen
sudo rm -rf /opt/eventscreen /etc/eventscreen /var/lib/eventscreen \
  /etc/systemd/system/eventscreen.service /etc/avahi/services/eventscreen.service
sudo userdel eventscreen
sudo systemctl enable getty@tty1
```
