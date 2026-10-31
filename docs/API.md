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
  never need it. `GET /info` reports `auth_required`.
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
| GET | `/` or `/idle` | The idle page (HTML) |

### GET /api/v1/info

```json
{
  "id": "screen-01",
  "name": "Pit lane board",
  "hostname": "screen-01",
  "version": "0.1.0",
  "api_version": "1",
  "browser": "cog",
  "auth_required": false,
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
    "last_error": ""
  }
}
```

- `state`: `idle` (empty playlist), `playing`, `paused` or `override`.
- `index`: position in the playlist, or `null` when it's empty.
- `current.ends_at`: when the page will change, or `null` if it won't on its
  own (paused, a single-item playlist, the idle page, or an open-ended override).
- `override`: `{"url", "until"}` while a `show` is active.
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

## Status codes

| Code | Meaning |
| --- | --- |
| 200 | OK |
| 400 | Bad body or field (see `error`) |
| 401 | Token missing or wrong |
| 404 | Unknown path |
| 405 | Wrong method for this path |
| 412 | `If-Match` revision didn't match |
| 413 | Body over 256 KB |
| 500 | Bug in the agent (logged in `journalctl -u eventscreen`) |

## Compatibility

New fields may be added to responses in v1; managers should ignore fields
they don't know. Removing or changing a field will mean a new `/api/v2`.
