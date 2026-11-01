# EventScreenDisplay

A small, lightweight web page viewer for Raspberry Pi screens at events. It
cycles through a playlist of web pages (typically live RCTC timing boards)
on a TV, and is controlled over the network through a simple HTTP API.

- Runs on a **Pi Zero 2 W** (minimum) or **Pi 3** over Wi-Fi, with Raspberry
  Pi OS Lite and no desktop, driving an HD 1920x1080 screen.
- **Fully unattended**: boots straight to its playlist with no keyboard or
  mouse, and reloads pages by itself after Wi-Fi or server outages.
- Uses **Cog (WPE WebKit)** straight on the display, with Chromium as a fallback.
- **Playlist API**: URLs with a dwell time each, next/previous/pause, temporary
  pages, status.
- **Found automatically** on the LAN via mDNS (`screen-01.local`,
  `_eventscreen._tcp`), ready for a central manager.
- Recovers on its own from power cuts, network drops, browser crashes and
  lock-ups (hardware watchdog).
- **New venue Wi-Fi from a phone**: with no known network, it starts a setup
  hotspot and shows how to join it on the TV.

## Quick start

On a Pi running Raspberry Pi OS Lite, install the `.deb` from the
[latest release](https://github.com/RCRaceHub/EventScreenDisplay/releases/latest)
and set the screen up:

```bash
sudo apt install ./eventscreen_*_all.deb
sudo eventscreen-setup --hostname screen-01 --name "Pit lane board"
sudo reboot
```

Or from a git checkout: `sudo ./deploy/install.sh --hostname screen-01`.

Then from any machine on the network:

```bash
curl -X PUT http://screen-01.local:8080/api/v1/playlist \
  -H "Content-Type: application/json" \
  -d '{"items": [{"url": "https://example.com/board", "duration": 30}]}'
```

## Docs

- [User guide](docs/user-guide.md): running screens at an event.
- [Installation](docs/installation.md): setting up a Pi, updating, troubleshooting.
- [Design and features](docs/design.md): how it works and why.
- [API reference](docs/API.md): the contract for the central manager.
- [Releasing](docs/releasing.md): versioning and publishing the `.deb`.
- [Apt repository](docs/apt-repository.md): proposed shared apt repo for RCRaceHub packages.

## Development

The agent is plain Python 3.11+ with no dependencies. Run it on any machine
without a display using the dummy browser:

```bash
python3 -m eventscreen --config deploy/config.example.toml \
  --browser dummy --state-dir ./state --port 8080
```

Run the tests:

```bash
python3 -m unittest -v
```

Build the Debian package (needs `dpkg-deb`):

```bash
packaging/build-deb.sh   # -> dist/eventscreen_<version>_all.deb
```

## Layout

```
eventscreen/   the viewer agent (Python package)
deploy/        files the package installs (service, config, eventscreen-setup) and install.sh
packaging/     build-deb.sh and the Debian maintainer scripts
.github/       CI: tests, package build, release upload
docs/          user, install, design and API docs
tests/         end-to-end API tests using the dummy browser
```
