# User guide

This is for whoever is running screens at an event. Until the central
manager in the RCTC timing system is ready, you control each screen with
simple web requests. The examples use `curl`, which is built into macOS,
Linux and Windows 10/11.

If your screens have a token set, add `-H "Authorization: Bearer YOURTOKEN"`
to every command that changes something (the ones with `-X`).

## Find your screens

Each screen shows its name and address on its idle screen. Screens answer
as `<hostname>.local`, for example `screen-01.local`.

To list every screen on the network:

- **Linux / Pi:** `avahi-browse -rt _eventscreen._tcp`
- **macOS:** `dns-sd -B _eventscreen._tcp`

## Check a screen

```bash
curl http://screen-01.local:8080/api/v1/status
```

`state` is one of:

- `idle`: no playlist, showing the idle screen.
- `playing`: cycling through the playlist.
- `paused`: holding on the current page.
- `override`: showing a temporary page (see below).

`current.url` is what's on screen now and `browser.running` should be `true`.

## Set what a screen shows

Send a playlist: a list of pages, each with how many seconds to stay on it.

```bash
curl -X PUT http://screen-01.local:8080/api/v1/playlist \
  -H "Content-Type: application/json" \
  -d '{
    "items": [
      {"url": "http://timing.local/board/live",    "duration": 60, "label": "Live timing"},
      {"url": "http://timing.local/board/results", "duration": 20, "label": "Results"},
      {"url": "http://timing.local/board/next",    "duration": 15, "label": "Next races"}
    ]
  }'
```

- `duration` is in seconds. Leave it out to use the default (30 seconds),
  or set `"default_duration"` alongside `"items"`.
- An item can just be a URL string: `"items": ["http://timing.local/board/live"]`.
- **One page only?** Send a single item. It stays up permanently and is
  never reloaded, so a live board keeps its connection.
- The playlist is saved, so the screen comes back to it after a power cut.

To clear the playlist and go back to the idle screen:

```bash
curl -X DELETE http://screen-01.local:8080/api/v1/playlist
```

## Control the rotation

```bash
curl -X POST http://screen-01.local:8080/api/v1/control/next       # skip to the next page
curl -X POST http://screen-01.local:8080/api/v1/control/previous   # go back one
curl -X POST http://screen-01.local:8080/api/v1/control/pause      # hold on this page
curl -X POST http://screen-01.local:8080/api/v1/control/resume     # carry on cycling
curl -X POST http://screen-01.local:8080/api/v1/control/reload     # refresh this page

# Jump to a page (counting from 0)
curl -X POST http://screen-01.local:8080/api/v1/control/goto \
  -H "Content-Type: application/json" -d '{"index": 2}'
```

## Show something for a moment

Put up a page temporarily, for example a podium or an announcement, then go
back to the playlist automatically:

```bash
curl -X POST http://screen-01.local:8080/api/v1/control/show \
  -H "Content-Type: application/json" \
  -d '{"url": "http://timing.local/podium", "duration": 120}'
```

Leave out `duration` to keep it up until you clear it:

```bash
curl -X DELETE http://screen-01.local:8080/api/v1/control/show
```

## Same playlist on several screens

```bash
for s in screen-01 screen-02 screen-03; do
  curl -X PUT http://$s.local:8080/api/v1/playlist \
    -H "Content-Type: application/json" -d @playlist.json
done
```

where `playlist.json` holds the `{"items": [...]}` body from above.

## Common questions

**The screen is black.** Give it 30 seconds after power-on. If it stays
black, check the TV input and that `status` answers. If `status` doesn't
answer, power-cycle the Pi.

**A page shows an error.** The screen loads pages just like a browser would.
Check the URL works from a laptop on the same network. When the page's
server comes back, `reload` it (or wait for it to come round again).

**Changes to a page aren't showing.** `reload` the screen.

**Can someone else change my screens?** Anyone on the same network can,
unless a token was set during install (`--token`). Ask whoever set up the
screens.

For every field and error code, see the [API reference](API.md).
