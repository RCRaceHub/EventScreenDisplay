# Design and features

EventScreenDisplay turns a Raspberry Pi and a TV into a screen that cycles
through web pages, mostly live RCTC timing boards. Each Pi runs a small
**viewer agent**. A central manager (planned, in the RCTC timing repo) finds
the screens on the LAN and tells each one what to show.

```
          ┌────────────────────────────┐
          │  Central manager (RCTC)    │   later; any HTTP client works today
          └──────┬──────────────┬──────┘
   mDNS browse   │  HTTP JSON   │
   _eventscreen  │  /api/v1     │
          ┌──────▼──────┐ ┌─────▼───────┐
          │ screen-01   │ │ screen-02   │   Raspberry Pi Zero 2 W / Pi 3
          │ agent :8080 │ │ agent :8080 │
          │   └─ Cog    │ │   └─ Cog    │   full-screen browser on HDMI
          └─────────────┘ └─────────────┘
```

## Goals

- Run well on a **Pi Zero 2 W** (512 MB RAM, the minimum) as well as a Pi 3,
  connected over **Wi-Fi**.
- Drive an **HD (1920x1080)** screen, the size the RCTC screens are designed for.
- **Boot and run without intervention**: no keyboard, mouse or person needed,
  from power-on onwards.
- Show **live JavaScript pages** (timing boards that update over
  WebSockets/polling) without them freezing or leaking.
- Be **controlled over the network** by a manager, with no keyboard or mouse.
- **Survive** power cuts, network drops and browser crashes without anyone
  touching the Pi.
- Be **simple to install** on stock Raspberry Pi OS Lite.

## Features

| Feature | How |
| --- | --- |
| Playlist of URLs, each with its own dwell time | `PUT /api/v1/playlist` |
| A single URL stays up forever without reloading | One-item playlists never re-navigate, so the board's live connection stays open |
| Next / previous / jump to item | `POST /api/v1/control/{next,previous,goto}` |
| Pause and resume the rotation | `POST /api/v1/control/{pause,resume}` |
| Temporarily show a page (an announcement, results), then return | `POST /api/v1/control/show` |
| Reload the current page | `POST /api/v1/control/reload` |
| Live status for monitoring | `GET /api/v1/status` |
| Playlist survives reboots | Saved to `/var/lib/eventscreen/playlist.json` |
| Self-healing browser | The agent restarts the browser if it exits, on the page that should be showing |
| Recovers from Wi-Fi or server outages | Checks the page's server every 10 s and reloads the page when it comes back |
| Fixed 1080p output | Installer pins the HDMI mode to 1920x1080 |
| Survives lock-ups | Hardware watchdog reboots a hung Pi |
| Idle screen showing the screen's name and address | Shown whenever the playlist is empty |
| Discovery on the LAN | Avahi advertises `_eventscreen._tcp` and `<hostname>.local` |
| Optional write protection | Bearer token for every change; reads stay open |
| Safe concurrent managers | Playlist revision + `ETag`/`If-Match` |

## Components

### Viewer agent (`eventscreen/`)

A single Python 3 program using only the standard library, so there is
nothing to `pip install` and it starts quickly on a Pi Zero.

- `player.py`: a background thread that works out what should be on screen
  and when to move on. It wakes every two seconds to check the browser is
  healthy, and immediately when a command arrives.
- `browsers.py`: drivers that start the kiosk browser and point it at a URL.
- `api.py`: the HTTP API and the idle page.
- `playlist.py`: validation and atomic save/load of the playlist.
- `config.py`: reads `/etc/eventscreen/config.toml`.

### Browser: Cog (WPE WebKit) by default

The browser is the heaviest part, so it decides what hardware works.

- **Cog / WPE WebKit** is the default. It is WebKit built for embedded
  screens and draws straight to the display through DRM/KMS, so no X11,
  Wayland compositor or desktop is needed. It uses a fraction of Chromium's
  memory, which is what makes the Pi Zero 2 W viable, and it runs modern
  JavaScript, WebSockets and CSS, which is all a timing board needs.
- **Chromium** in kiosk mode (inside the `cage` compositor) is the fallback
  for a page that WebKit renders badly. It is comfortable on a Pi 3 but tight
  on a Zero 2 W's 512 MB, especially with several heavy pages.
- **dummy** runs no browser at all, for development and tests.

Changing page reuses the running browser: Cog is told over D-Bus with
`cogctl open`, and Chromium through its local DevTools HTTP endpoints. This
is faster than restarting the browser and avoids a black flash.

### Why navigate instead of keeping every page loaded

An alternative design keeps every playlist page open in hidden frames and
just swaps which one is visible. That makes switching instant and keeps
every board live, but it multiplies memory use by the number of pages and
breaks on any site that refuses to be framed. On a 512 MB Pi that is the
wrong trade, so the agent loads one page at a time. Each page loads fresh
when it comes round, which also clears any slow memory leak in a
long-running board.

### Unattended operation

The screen has to look after itself from power-on, so several layers each
cover a different failure:

1. **Network outages.** On Wi-Fi the first page often loads before the
   network is up, and a timing server can restart mid-event. The browser is
   then stuck on an error page; a single-page playlist would never move on to
   fix it. A small background thread in the player tries a TCP connection to
   the page's server every 10 seconds and reloads the page when it goes from
   unreachable to reachable. It doesn't reload while the server is fine, so a
   live board's own reconnect logic is left alone. The result is in
   `status.network`.
2. **Browser crashes.** The player checks the browser process every 2 seconds
   and restarts it on the right page.
3. **Agent crashes.** systemd restarts the service.
4. **Kernel or hardware hangs.** systemd's hardware watchdog support
   (`RuntimeWatchdogSec=15`) reboots the Pi.
5. **Wi-Fi power saving** is turned off, since it causes dropouts and missed
   mDNS on the Pi's Wi-Fi chip.

The display is pinned to 1920x1080 at the kernel level (`video=` in
`cmdline.txt`), so it doesn't depend on what the TV advertises. Many TVs
advertise 4K first, which a Pi Zero 2 W can't render smoothly.

### Discovery (mDNS)

`install.sh` sets the hostname (for example `screen-01`) and installs an
Avahi service file. Every screen then answers as `screen-01.local` and
advertises:

- `_eventscreen._tcp` on the API port, with TXT records `id`, `api=/api/v1`
  and `version`. This is what a manager browses for.
- `_http._tcp`, so the screen also shows up in generic service browsers.

Avahi ships with Raspberry Pi OS, so the agent doesn't need its own mDNS code.

Every screen advertising the same service type is expected and is how
DNS-SD works: the type (`_eventscreen._tcp`) is shared, and each screen
publishes its own instance under it, named `EventScreen <hostname>`, with its
own address. A manager browsing the type gets one result per screen. The
only clash is two screens with the **same hostname**, usually from cloning
an SD card. Avahi then renames the newcomer (`screen-01-2.local`,
`EventScreen screen-01 #2`), so both keep working, but it's confusing. Give
every screen its own `--hostname` (and so its own `id`), and a manager
should key screens on `id` rather than on the instance name.

### Running as a service

The `eventscreen` systemd unit runs the agent as an unprivileged
`eventscreen` user with access to the display (`video`, `render`, `input`
groups). It's wrapped in `dbus-run-session` so the agent and Cog share a
private D-Bus session bus. systemd restarts the agent if it dies, and the
agent restarts the browser if it dies.

## Designed for a central manager

The API is meant to be driven by the RCTC manager, so it follows a few rules:

- **Versioned** under `/api/v1`, and `GET /api/v1/info` reports the agent
  and API versions so a manager can handle mixed fleets.
- **Declarative playlist.** The manager `PUT`s the whole playlist; the screen
  doesn't need to remember edits. Repeating the same `PUT` is harmless.
- **Revisions.** Every change bumps `revision`, which the manager can compare
  with what it last sent to detect drift (someone changed a screen by hand,
  or the Pi was swapped). `If-Match` stops two managers overwriting each other.
- **`source` tag.** The manager can stamp the playlist with an id of its own
  (for example an event or layout id) and read it back from `/status`.
- **Stable `id`.** Each screen has a `screen_id` (defaults to the hostname)
  that the manager can key on, independent of IP address.
- **CORS enabled**, so a manager UI running in a browser can call screens
  directly.

See [API.md](API.md) for the full reference.

## Limits and future ideas

- Screenshots of what is on screen are not supported yet. Cog on DRM has no
  easy capture path; this could be added for the Chromium driver.
- Per-item zoom, screen rotation from the API, and scheduled playlists
  (switch at a time of day) are natural next steps.
- There is no TLS on the local API. It's meant for a trusted event LAN; set
  `api_token` if others share that network.
