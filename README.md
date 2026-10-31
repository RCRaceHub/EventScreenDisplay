# EventScreenDisplay

A small, lightweight web page viewer for Raspberry Pi screens at events. It
cycles through a playlist of web pages (typically live RCTC timing boards)
on a TV, and is controlled over the network through a simple HTTP API.

- Runs on a **Pi Zero 2 W** or **Pi 3** with Raspberry Pi OS Lite, no desktop.
- Uses **Cog (WPE WebKit)** straight on the display, with Chromium as a fallback.
- **Playlist API**: URLs with a dwell time each, next/previous/pause, temporary
  pages, status.
- **Found automatically** on the LAN via mDNS (`screen-01.local`,
  `_eventscreen._tcp`), ready for a central manager.
- Recovers on its own from reboots, network drops and browser crashes.

## Quick start

On a Pi running Raspberry Pi OS Lite:

```bash
git clone https://github.com/RCRaceHub/EventScreenDisplay.git
cd EventScreenDisplay
sudo ./deploy/install.sh --hostname screen-01 --name "Pit lane board"
sudo reboot
```

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

## Layout

```
eventscreen/   the viewer agent (Python package)
deploy/        install.sh, systemd unit, mDNS service, example config
docs/          user, install, design and API docs
tests/         end-to-end API tests using the dummy browser
```
