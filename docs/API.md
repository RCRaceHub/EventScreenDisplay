# API reference (v1)

Every screen serves a JSON API on port 8080 (configurable) at
`http://<hostname>.local:8080/api/v1`. This document is the contract the
central manager builds against.

## Conventions

- Request and response bodies are JSON (`Content-Type: application/json`).
- Times are Unix timestamps in seconds (floats). Durations are whole seconds.
- Errors return a non-2xx status with `{"error": "message"}`.
- **Auth.** If the screen has `api_token` set, every `PUT`, `POST` and
  `DELETE` needs `Authorization: Bearer <token>`, or it gets `401`. `GET`s
  never need it. `GET /info` reports `auth_required`. Wi-Fi changes are the
  exception: they are refused (`403`) unless a token is set, because a bad
  change can take a screen off the network.
- **CORS** is open (`Access-Control-Allow-Origin: *`), so a manager running
  in a browser can call screens directly.
- Every control endpoint returns the same body as `GET /status`, so the
  caller sees the effect immediately.

## Discovery

Screens advertise over mDNS (DNS-SD):

| Service | Port | TXT records |
| --- | --- | --- |
| `_eventscreen._tcp` | API port | `id=<screen_id>`, `api=/api/v1`, `version=<agent version>` |
| `_http._tcp` | API port | `path=/` |

A manager should browse `_eventscreen._tcp`, then call `GET /api/v1/info` on
each result. Key screens by `id`, not by IP or hostname.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/v1/info` | Identity and capabilities |
| GET | `/api/v1/status` | What's on screen now |
| GET | `/api/v1/playlist` | The current playlist |
| PUT | `/api/v1/playlist` | Replace the playlist |
| DELETE | `/api/v1/playlist` | Clear the playlist (show the idle page) |
| POST | `/api/v1/control/next` | Next item |
| POST | `/api/v1/control/previous` | Previous item |
| POST | `/api/v1/control/goto` | Jump to an item |
| POST | `/api/v1/control/pause` | Stop rotating |
| POST | `/api/v1/control/resume` | Start rotating again |
| POST | `/api/v1/control/reload` | Reload the current page |
| POST | `/api/v1/control/show` | Temporarily show a URL |
| DELETE | `/api/v1/control/show` | End the temporary URL |
| GET | `/api/v1/wifi` | Wi-Fi connection and saved networks |
| GET | `/api/v1/wifi/scan` | Wi-Fi networks the screen can see |
| POST | `/api/v1/wifi/networks` | Save a network, and optionally switch to it |
| DELETE | `/api/v1/wifi/networks/{ssid}` | Forget a saved network |
| GET | `/` or `/idle` | The idle page (HTML) |

### GET /api/v1/info

```json
{
  "id": "screen-01",
  "name": "Pit lane board",
  "hostname": "screen-01",
  "version": "0.4.0",
  "api_version": "1",
  "browser": "cog",
  "auth_required": false,
  "features": ["playlist", "control", "wifi"],
  "limits": {"default_duration": 30, "min_duration": 5}
}
```

### GET /api/v1/status

```json
{
  "id": "screen-01",
  "uptime": 3605.2,
  "time": 1793408728.56,
  "state": "playing",
  "paused": false,
  "index": 1,
  "current": {
    "url": "http://timing.local/board/results",
    "label": "Results",
    "id": "results",
    "started_at": 1793408710.1,
    "ends_at": 1793408730.1
  },
  "override": null,
  "network": {"page_reachable": true, "checked_at": 1793408725.0},
  "playlist": {"revision": 4, "length": 3, "updated_at": 1793405000.0, "source": "event-42"},
  "browser": {
    "driver": "cog",
    "running": true,
    "pid": 812,
    "url": "http://timing.local/board/results",
    "restarts": 0,
    "last_error": "",
    "preload": true
  }
}
```

- `state`: `idle` (empty playlist), `playing`, `paused` or `override`.
- `index`: position in the playlist, or `null` when it's empty.
- `current.ends_at`: when the page will change, or `null` if it won't on its
  own (paused, a single-item playlist, the idle page, or an open-ended override).
- `override`: `{"url", "until"}` while a `show` is active.
- `browser.preload`: `true` when the page is shown on the preloading stage
  (instant switches), `false` when the browser was sent to it directly
  (preload off, or the site refuses to be framed). Absent with `preload = false`.
- `network.page_reachable`: whether the screen could open a TCP connection to
  the server of the page on screen at `network.checked_at` (checked every 10
  seconds). `null` before the first check and on the idle page. When it goes
  from `false` to `true` the screen reloads the page by itself.
- `browser.restarts` counts browser crashes recovered since the agent started;
  a climbing number is worth alerting on.

`time` is the screen's clock, so a manager can spot a Pi with a wrong clock.

### GET /api/v1/playlist

```json
{
  "items": [
    {"url": "http://timing.local/board/live", "duration": 60, "label": "Live timing", "id": "live"}
  ],
  "revision": 4,
  "updated_at": 1793405000.0,
  "source": "event-42"
}
```

The response carries `ETag: "<revision>"`.

### PUT /api/v1/playlist

Replaces the whole playlist and starts it from `start_index`.

```json
{
  "items": [
    {"url": "http://timing.local/board/live", "duration": 60, "label": "Live timing", "id": "live"},
    "http://timing.local/board/results"
  ],
  "default_duration": 20,
  "start_index": 0,
  "source": "event-42"
}
```

| Field | Required | Notes |
| --- | --- | --- |
| `items` | yes | Array. Each entry is a URL string or an object. Empty array = idle. |
| `items[].url` | yes | Absolute `http://` or `https://` URL. |
| `items[].duration` | no | Seconds, at least `limits.min_duration`. Defaults to `default_duration`. |
| `items[].label` | no | Free text, shown in status. |
| `items[].id` | no | Manager's own id for the item, echoed in status. |
| `default_duration` | no | Default for items in this request. Falls back to the screen's config. |
| `start_index` | no | Item to start on. Default 0. |
| `source` | no | Free text the manager can use to tag the playlist. |

Returns the saved playlist (as `GET`), with its new `revision` and `ETag`.

**Concurrency.** Send `If-Match: "<revision>"` to only replace the playlist
if it's still the one you last saw. If it changed, you get `412` and should
re-read it.

Errors: `400` with a message naming the bad item (e.g.
`items[1]: url must be an absolute http(s) URL`), `401`, `412`.

### DELETE /api/v1/playlist

Clears the playlist (bumping `revision`) and shows the idle page.

### POST /api/v1/control/next, /previous

Move one item forward or back (wrapping round) and restart the dwell timer.
These also end any active `show`.

### POST /api/v1/control/goto

```json
{"index": 2}
```

`400` if `index` is outside the playlist.

### POST /api/v1/control/pause, /resume

`pause` holds the current page indefinitely. `resume` restarts the dwell
timer on the current page and carries on. The paused state is not saved
across restarts; a rebooted screen always resumes playing.

### POST /api/v1/control/reload

Reloads the page currently on screen.

### POST /api/v1/control/show

Shows a URL over the playlist, then returns to where the playlist was.

```json
{"url": "http://timing.local/podium", "duration": 120, "label": "Podium"}
```

Without `duration` the page stays until `DELETE /api/v1/control/show`,
`next`, `previous` or `goto`. Not saved across restarts.

### DELETE /api/v1/control/show

Ends the temporary page and returns to the playlist.

## Wi-Fi

These endpoints manage the screen's Wi-Fi networks. The agent hands them to
the `eventscreen-wifi` service, which runs as root and talks to
NetworkManager. If that service isn't running they return `503`. Every write
needs the bearer token, and is refused with `403` when the screen has no
`api_token`. Passwords are never returned.

The usual way to move a screen to a new venue is to **save the venue's
network ahead of time** (`connect` left off). The screen keeps every saved
network and joins whichever one it can see, so it connects at the venue by
itself. If it finds none, it falls back to its setup hotspot (see the
[user guide](user-guide.md#moving-a-screen-to-a-new-wi-fi-network)).

### GET /api/v1/wifi

```json
{
  "mode": "client",
  "connected": {"connection": "wifi-Home", "ssid": "Home", "signal": 67, "ip": "192.168.1.50"},
  "saved": [
    {"ssid": "Venue", "priority": 10, "active": false},
    {"ssid": "Home", "priority": 0, "active": true}
  ],
  "hotspot": {"ssid": "EventScreen-screen-01", "enabled": true, "after": 120},
  "connecting": null,
  "last_change": {"ssid": "Home", "ok": true, "message": "", "at": 1761955200.0}
}
```

- `mode` is `client` normally, or `hotspot` while the screen is in Wi-Fi
  setup mode (then `connected` is `null`). In practice you'll only see
  `hotspot` from a device joined to the hotspot.
- `saved` is highest priority first. When several saved networks are in
  range, the screen joins the one with the highest priority.
- `connecting` is the SSID being joined right now, if any.
- `last_change` is the result of the last `connect: true` request since the
  service started, or `null`.

### GET /api/v1/wifi/scan

```json
{"networks": [{"ssid": "Venue", "signal": 70, "secure": true}]}
```

Strongest first, one entry per SSID, without the screen's own hotspot. A
scan takes a few seconds. In setup mode it returns the scan made just before
the hotspot started.

### POST /api/v1/wifi/networks

```json
{"ssid": "Venue", "password": "venue-password", "priority": 10, "hidden": false, "connect": false}
```

| Field | Default | |
| --- | --- | --- |
| `ssid` | required | 1 to 32 bytes |
| `password` | `""` | WPA2/WPA3-Personal password, 8 to 63 characters. Empty for an open network |
| `priority` | `0` | -999 to 999. Higher wins when several saved networks are in range |
| `hidden` | `false` | The network doesn't broadcast its name |
| `connect` | `false` | Switch to it now |

Saving an SSID that's already saved replaces it, which is how to change a
password or priority.

- Without `connect`: `201` with `{"ssid": "Venue", "saved": true, "connecting": false}`.
  The current connection isn't touched.
- With `connect: true`: `202` with `"connecting": true`, sent **before** the
  switch, because the screen drops off the current network while it
  switches (for 45 seconds at most). If the new network is a different LAN,
  you won't be able to reach the screen from the old one afterwards. Check
  the result later with `GET /wifi` (`last_change`). If the join fails,
  NetworkManager goes back to the other saved networks by itself.

### DELETE /api/v1/wifi/networks/{ssid}

Forgets a saved network. Percent-encode the SSID in the path (`My%20Net`).
Returns `{"ssid": "Venue", "forgotten": true}`, `404` if it isn't saved, or
`409` if it's the network the screen is on now: connect it to another
network first, so it can't strand itself.

## Status codes

| Code | Meaning |
| --- | --- |
| 200 | OK |
| 400 | Bad body or field (see `error`) |
| 201 | Wi-Fi network saved |
| 202 | Wi-Fi network saved; switching to it now |
| 401 | Token missing or wrong |
| 403 | Wi-Fi change on a screen with no `api_token` |
| 404 | Unknown path |
| 405 | Wrong method for this path |
| 409 | Can't forget the Wi-Fi network the screen is on |
| 412 | `If-Match` revision didn't match |
| 413 | Body over 256 KB |
| 500 | Bug in the agent (logged in `journalctl -u eventscreen`) |
| 503 | The Wi-Fi service isn't running (`journalctl -u eventscreen-wifi`) |

## Compatibility

New fields may be added to responses in v1; managers should ignore fields
they don't know. Removing or changing a field will mean a new `/api/v2`.
