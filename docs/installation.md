# Installing on a Raspberry Pi

This sets up a Pi as a screen that boots straight into the viewer and runs
with no keyboard, mouse or person attending it. Everything is done from a
laptop: the SD card is prepared with Wi-Fi and SSH already set, and the rest
is done over SSH. Allow about 20 minutes per Pi the first time, most of it
writing the SD card and downloading packages.

## What you need

- Raspberry Pi **Zero 2 W** (the minimum) or a **Pi 3 B/B+**. Both have
  built-in Wi-Fi. A Pi 4 or 5 works too. The original Pi Zero and Zero W are
  not supported: they are too slow for live pages.
- 8 GB or larger microSD card (A1/A2 rated cards boot faster).
- An HD (1920x1080) TV or monitor on HDMI. The Zero 2 W needs a mini-HDMI
  adapter or cable.
- A good power supply: the official 5V 2.5A supply for the Zero 2 W or Pi 3.
  Under-powered Pis brown out, drop Wi-Fi and corrupt SD cards.
- A Wi-Fi network on the same LAN as the RCTC timing system. 2.4 GHz only
  for the Zero 2 W and Pi 3 B; the Pi 3 B+ also does 5 GHz.
- No keyboard or mouse is needed at any point.

## 1. Write the SD card

1. Install [Raspberry Pi Imager](https://www.raspberrypi.com/software/).
2. Choose **Raspberry Pi OS Lite (64-bit)** (Bookworm). For a Pi Zero 2 W the
   64-bit Lite image is fine; no desktop is needed.
3. Open the settings (the cog, or Ctrl+Shift+X) and set:
   - **Hostname**: the screen's name, for example `screen-01`. Use a
     different name for every screen.
   - **Username and password** for yourself.
   - **Wi-Fi** network, password and country. This is how the Pi gets on
     the network with nobody at its keyboard, so double-check it.
   - **Enable SSH**.
4. Write the card, put it in the Pi, connect the screen and power it on.

## 2. Install the viewer

The viewer comes as a Debian package, `eventscreen`. From your laptop, on the
same network:

```bash
ssh <you>@screen-01.local
# Download the latest .deb from the GitHub releases page:
#   https://github.com/RCRaceHub/EventScreenDisplay/releases/latest
wget https://github.com/RCRaceHub/EventScreenDisplay/releases/download/v0.2.0/eventscreen_0.2.0_all.deb
sudo apt-get update
sudo apt install ./eventscreen_0.2.0_all.deb
sudo eventscreen-setup --hostname screen-01 --name "Pit lane board"
sudo reboot
```

Change `0.2.0` to the latest version on the releases page. Once the RCRaceHub
apt repository is set up (see [apt-repository.md](apt-repository.md)) this
becomes `sudo apt install eventscreen`.

After the reboot the TV shows the idle screen with the screen's name and API
address. It's ready for a playlist; see the [user guide](user-guide.md).
From now on the Pi only needs power and HDMI: it comes straight back to its
playlist every time it's switched on.

### Installing from a git checkout instead

For testing unreleased changes, `deploy/install.sh` builds the package from
the source, installs it and runs `eventscreen-setup` with the same options:

```bash
sudo apt-get install -y git
git clone https://github.com/RCRaceHub/EventScreenDisplay.git
cd EventScreenDisplay
sudo ./deploy/install.sh --hostname screen-01 --name "Pit lane board"
```

### eventscreen-setup options

Run `sudo eventscreen-setup` again at any time to change these. Options you
leave out stay as they are.

| Option | Meaning |
| --- | --- |
| `--hostname NAME` | Sets the Pi's hostname, so it is reachable as `NAME.local`. |
| `--name "TEXT"` | Friendly name shown on the idle screen and in the API. |
| `--browser cog\|chromium` | Browser to use. Default `cog` (recommended). `chromium` installs Chromium and the `cage` compositor. |
| `--port N` | API port. Default 8080. |
| `--token SECRET` | Require `Authorization: Bearer SECRET` for any change. |
| `--resolution MODE` | HDMI mode. Default `1920x1080@60`. `auto` uses whatever the TV prefers. Add rotation like `1920x1080@60,rotate=90`. |

### What gets installed

**The package** (`apt install`) does everything that is the same on every Pi:

1. Pulls in `python3`, `dbus`, `avahi-daemon` and, as recommended packages,
   the `cog` browser, `libnss-mdns` and a font.
2. Installs the agent to `/usr/lib/eventscreen` and the `eventscreen`
   systemd service, then enables and starts it.
3. Creates the `eventscreen` system user with access to the display.
4. Installs `/etc/eventscreen/config.toml`. apt treats it as a config file,
   so upgrades never overwrite your changes.
5. Turns off Wi-Fi power saving, which otherwise causes dropouts on the Pi's
   Wi-Fi chip.
6. Enables the hardware watchdog, so the Pi reboots itself if it ever locks up
   (takes effect after a reboot).
7. Advertises the screen over mDNS. The service writes
   `/etc/avahi/services/eventscreen.service` from the config each time it
   starts, so the advertised port and id always match.

**`eventscreen-setup`** does what changes per Pi, or touches the Pi's boot
settings, which a package shouldn't do on its own:

1. Sets the hostname if asked.
2. Fixes the HDMI output at 1920x1080 (`video=HDMI-A-1:1920x1080@60` in
   `cmdline.txt`), stops the console blanking (`consoleblank=0`) and takes the
   login prompt off the HDMI console (`getty@tty1` disabled; SSH still works).
3. Writes your options into `/etc/eventscreen/config.toml` and restarts the
   viewer. It tells you when a reboot is needed.

## Running unattended

Once installed, nobody needs to touch the Pi. This is what happens when
things go wrong:

| Event | What the screen does |
| --- | --- |
| Power on | Boots to the saved playlist. Shows the idle screen if there's no playlist yet. |
| Power cut | Same as power on. The playlist is saved to disk. |
| Wi-Fi not up yet at boot, or drops | The browser may show an error page. The agent checks the page's server every 10 seconds and reloads the page as soon as it can be reached. |
| Timing server restarts | Same as above: reloads the page when the server answers again. |
| Browser crashes | Restarted within a few seconds on the page that should be showing. |
| Agent crashes | systemd restarts it within 3 seconds. |
| Pi locks up completely | The hardware watchdog reboots it within about 15 seconds. |

Pulling the power is a normal way to switch a screen off. SD cards do
occasionally get corrupted by power cuts, so keep a spare card imaged for
each screen at events.

### Changing the Wi-Fi network

Do this before taking a screen to a new venue, while it can still reach your
current network:

```bash
ssh <you>@screen-01.local
sudo nmcli connection add type wifi con-name venue ifname wlan0 \
  ssid "VenueWiFi" wifi-sec.key-mgmt wpa-psk wifi-sec.psk "password"
```

The Pi keeps both networks and joins whichever it can see. If it's already at
the venue and can't connect, re-write the SD card with the new Wi-Fi details
(step 1) and install again.

## Updating

Download the new `.deb` from the releases page and install it over the top:

```bash
sudo apt install ./eventscreen_0.3.0_all.deb
```

Your config and the current playlist are kept, and the service restarts on
the new version. There's no need to run `eventscreen-setup` again. From a git
checkout, `git pull && sudo ./deploy/install.sh` does the same.

**Upgrading from 0.1.0** (installed by the old script into `/opt`): run
`git pull && sudo ./deploy/install.sh` once. It removes the old files and
installs the package, keeping your config and playlist.

## Files and locations

| Path | What |
| --- | --- |
| `/usr/lib/eventscreen/` | The agent code |
| `/usr/sbin/eventscreen-setup` | Per-Pi setup command |
| `/etc/eventscreen/config.toml` | Configuration (see comments inside) |
| `/var/lib/eventscreen/playlist.json` | The saved playlist |
| `/usr/lib/systemd/system/eventscreen.service` | The service |
| `/etc/avahi/services/eventscreen.service` | mDNS advertisement (generated from the config) |
| `/etc/NetworkManager/conf.d/eventscreen-wifi-powersave.conf` | Wi-Fi power saving off |
| `/usr/lib/systemd/system.conf.d/eventscreen-watchdog.conf` | Hardware watchdog |
| `/usr/share/doc/eventscreen/` | These docs |

## Display tips

- **Resolution.** `eventscreen-setup` fixes the output at 1920x1080, which is what
  the RCTC screens are designed for and stops a 4K TV overloading a Pi Zero.
  To change it, run `sudo eventscreen-setup --resolution 1280x720@60` (or
  `auto`) and reboot.
- **Rotation (portrait screens).** Run
  `sudo eventscreen-setup --resolution 1920x1080@60,rotate=90` and reboot.
- **Text too small or large.** Add Cog's scale option in the config, for
  example `browser_args = ["--scale=1.5"]`, then restart the service.
- **Overscan (black border or cropped edges on old TVs).** Turn off overscan
  on the TV ("Just scan", "Screen fit" or "1:1").

## Troubleshooting

| Symptom | Check |
| --- | --- |
| Screen stays on the text console | `journalctl -u eventscreen -e` for errors; `systemctl status eventscreen` |
| `screen-01.local` doesn't resolve | Use the IP (`hostname -I` on the Pi). Some Windows PCs and guest Wi-Fi block mDNS. `systemctl status avahi-daemon` |
| A page is blank or looks wrong in Cog | Try `sudo eventscreen-setup --browser chromium` to see if it's a WebKit issue |
| Pi Zero reboots or freezes | Usually power. Check `vcgencmd get_throttled` (anything but `0x0` means under-voltage or throttling) |
| Screen shows "No signal" | The TV may not accept 1080p at 60 Hz over that cable or adapter. Try `sudo eventscreen-setup --resolution 1920x1080@50` or `auto` |
| `screen-01.local` vanishes now and then | Check Wi-Fi signal (`nmcli dev wifi` on the Pi). Move the Pi out from behind the TV, which blocks Wi-Fi |
| Page loads but stops updating | `POST /api/v1/control/reload`, and check the timing server is reachable from the Pi |

Live logs: `journalctl -u eventscreen -f`. For more detail, run
`sudo systemctl edit eventscreen` and add:

```ini
[Service]
ExecStart=
ExecStart=/usr/bin/dbus-run-session -- /usr/bin/python3 -m eventscreen --config /etc/eventscreen/config.toml -v
```

then `sudo systemctl restart eventscreen`. Remove it again with
`sudo systemctl revert eventscreen`.

## Uninstalling

```bash
sudo apt purge eventscreen
sudo systemctl enable getty@tty1
```

`purge` removes the config and playlist too; `apt remove` keeps them. The
hostname and display settings in `cmdline.txt` are left as they are.
