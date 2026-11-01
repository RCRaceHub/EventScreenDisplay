# User guide

This is for whoever is running screens at an event. Until the central
manager in the RCTC timing system is ready, you control each screen with
simple web requests. The examples use `curl`, which is built into macOS,
Linux and Windows 10/11.

If your screens have a token set, add `-H "Authorization: Bearer YOURTOKEN"`
to every command that changes something (the ones with `-X`).

## How the screens behave

- Screens show pages at **1920x1080**, so RCTC screens designed for HD fit
  exactly.
- A screen only needs power and HDMI. Switch it on and it comes back to its
  last playlist by itself; just pull the power to switch it off.
- If the Wi-Fi or the timing server goes down, the screen shows an error
  page and then reloads by itself within about 10 seconds of the server
  being reachable again.
- If the screen can't find any Wi-Fi it knows for 2 minutes, for example at
  a new venue, it shows **Wi-Fi setup** on the TV instead. See
  [Moving a screen to a new Wi-Fi network](#moving-a-screen-to-a-new-wi-fi-network).

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

## Moving a screen to a new Wi-Fi network

You only need a phone. Switch the screen on at the venue. If it can't join
any Wi-Fi it knows, after about 2 minutes the TV shows:

- the name of the screen's own setup hotspot, `EventScreen-<screen name>`,
- the hotspot's password,
- the setup address, `http://10.42.0.1`.

Then:

1. On your phone, join the hotspot with the password from the TV.
2. The setup page usually opens by itself. If not, open
   `http://10.42.0.1` in the phone's browser. If the phone warns that the
   hotspot has no internet, choose to stay connected.
3. Pick the venue's network from the list, or type its name (tick
   **Hidden network** if it doesn't broadcast its name). Type the password,
   or leave it empty for an open network, and press **Connect**.
4. Your phone drops off the hotspot, which is expected. If the screen goes
   back to its pages, it worked. The screen remembers the network from now on.
5. If the password was wrong or the network is out of range, the hotspot and
   the setup message come back within about a minute, with the reason on the
   TV. Join it again and retry.

### Adding the venue's Wi-Fi before you go

This is easier than using the hotspot. While the screen is still on your
current network, save the venue's network, and the screen joins it by itself
when it arrives. The screen needs a token for this (see below):

```bash
curl -X POST http://screen-01.local:8080/api/v1/wifi/networks \
  -H "Authorization: Bearer YOURTOKEN" -H "Content-Type: application/json" \
  -d '{"ssid": "VenueWiFi", "password": "venue-password"}'
```

To see what a screen is connected to and which networks it has saved:

```bash
curl http://screen-01.local:8080/api/v1/wifi
```

Add `"connect": true` to switch over straight away, for example when the
venue network is already in range. The screen drops off for up to 45
seconds while it switches, and if the new network isn't the one your laptop
is on you won't be able to reach the screen afterwards. To forget a network:

```bash
curl -X DELETE http://screen-01.local:8080/api/v1/wifi/networks/OldVenue \
  -H "Authorization: Bearer YOURTOKEN"
```

Changing Wi-Fi over the network only works on screens set up with a token
(`sudo eventscreen-setup --token ...`), so nobody else on a shared network
can take your screens off it. Without a token, use the hotspot.

Screens remember every network they've joined, so a screen moving between
your usual venues finds the right one by itself. If the venue Wi-Fi just
wasn't ready when the screen was switched on, you don't need to do anything:
the screen retries the networks it knows every 5 minutes.

## Common questions

**The screen is black.** Give it 30 seconds after power-on. If it stays
black, check the TV input and that `status` answers. If `status` doesn't
answer, power-cycle the Pi. If you can SSH in, `sudo eventscreen-check`
says what's wrong.

**A page shows an error.** The screen loads pages just like a browser would.
Check the URL works from a laptop on the same network. When the page's
server comes back, `reload` it (or wait for it to come round again).

**Changes to a page aren't showing.** `reload` the screen.

**RCTC runs in Docker and can't find the screens.** Containers on Docker's
normal (bridge) network can't see mDNS, so they can't discover screens or
look up `screen-01.local`. Docker Desktop on Windows and Mac has the same
problem even with host networking. Add screens by IP instead,
`http://192.168.1.50:8080` (find it with `hostname -I` on the Pi), and give
each Pi a DHCP reservation on the router so its IP doesn't change. On a
Linux Docker host, `network_mode: host` makes discovery work. The other
direction matters too: the board URLs you send to a screen must use the
Docker host's LAN IP and the published port, never `localhost` or the
container's name, and the host's firewall must let the Pi in.

**Can someone else change my screens?** Anyone on the same network can,
unless a token was set during install (`--token`). Ask whoever set up the
screens.

For every field and error code, see the [API reference](API.md).
