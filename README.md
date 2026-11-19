# EventScreenDisplay

A small, lightweight web page viewer for Raspberry Pi screens at events. It
cycles through a playlist of web pages (typically live RCTC timing boards)
on a TV, and is controlled over the network through a simple HTTP API.

- Runs on a **Pi Zero 2 W** (minimum) or **Pi 3** over Wi-Fi, with Raspberry
  Pi OS Lite and no desktop, driving an HD 1920x1080 screen.
- **Fully unattended**: boots straight to its playlist with no keyboard or
  mouse, and reloads pages by itself after Wi-Fi or server outages.
- Uses **Cog (WPE WebKit)** straight on the display, with Chromium as a fallback.
- **One small static binary** (Go, about 7 MB) with nothing else to install:
  no Python or other runtime on the Pi.
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
(`arm64` for 64-bit Raspberry Pi OS, `armhf` for 32-bit) and set the screen up:

```bash
sudo apt install ./eventscreen_*_$(dpkg --print-architecture).deb
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

The agent is Go (1.24 or later) with one dependency, a TOML parser. Run it on
any machine without a display using the dummy browser:

```bash
go run ./cmd/eventscreen --config deploy/config.example.toml \
  --browser dummy --state-dir ./state --port 8080
```

Run the tests:

```bash
make test       # go test ./...
make race       # with the race detector, as CI does
make cover      # and a coverage total
make lint       # gofmt, go vet and shellcheck
```

They cover the HTTP API end to end (with the dummy browser), playlist
rotation, preloading, event messages and banners, browser restarts and
back-off, recovery when a page's server comes back, the Wi-Fi setup hotspot
and Wi-Fi API (with a fake NetworkManager), `eventscreen-setup` (against a
scratch root), the `.deb` build, `eventscreen check`, config and playlist
handling. CI runs them for every push and pull request, as separate checks
from the package builds. See [docs/releasing.md](docs/releasing.md#checks).

On a real Pi, `sudo eventscreen-check` checks the installed screen.

Build binaries and packages (cross-compiled, fully static; the `.deb`s need
`dpkg-deb`):

```bash
make arm64 armhf amd64   # -> dist/bin/eventscreen-linux-<arch>
make debs                # -> dist/eventscreen_<version>_<arch>.deb
make deb-arm64           # just one
```

### Testing without a Pi (Docker)

The `Dockerfile` builds a minimal image (scratch plus the binary, about
10 MB) that runs the agent headless with the dummy browser. It answers the
whole API, so it's handy for developing a screen manager such as RCTC's:

```bash
make docker              # or: docker build -t eventscreen:dev .
docker run --rm -p 8080:8080 eventscreen:dev
curl http://localhost:8080/api/v1/status
```

Run several on different ports to fake a fleet. To set `screen_id`, `name`
or `api_token`, mount a config at `/etc/eventscreen/config.toml` (keep
`browser = "dummy"`). Nothing is drawn, and Wi-Fi and mDNS aren't there, so
add these screens to a manager by address.

## Layout

```
cmd/eventscreen/   the binary: run, wifi, avahi, check and version subcommands
internal/          the agent's packages (api, player, stage, browser, messages,
                   playlist, config, wifi, avahi, check)
deploy/            files the package installs (services, config, eventscreen-setup) and install.sh
packaging/         build-deb.sh and the Debian maintainer scripts
.github/           CI: tests, lint, package builds, Docker image, release upload
docs/              user, install, design and API docs
```
