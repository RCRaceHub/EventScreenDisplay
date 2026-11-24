# SD card image

The quickest way to make a screen: write the EventScreen image to an SD
card with Raspberry Pi Imager, put it in the Pi and switch it on. There's
nothing to install over SSH. Each release has the image attached, built from
the official Raspberry Pi OS Lite (64-bit) with the `eventscreen` package,
the Cog browser and everything it needs already installed, and the display
already set up for a kiosk (1080p forced on HDMI, no console blanking, no
login prompt on the TV).

It suits the **Pi Zero 2 W**, **Pi 3**, Pi 4 and Pi 5. See
[installation.md](installation.md#what-you-need) for the rest of the
hardware.

## Write a card

### With EventScreen listed in Imager (recommended)

Imager can list EventScreen next to Raspberry Pi OS, which keeps its
settings screen (hostname, user, Wi-Fi, SSH) working for the image. Once per
laptop:

1. Install [Raspberry Pi Imager](https://www.raspberrypi.com/software/)
   2.0 or later.
2. Open **App Options** → **Content Repository** → **Edit**, choose **Use
   custom URL** and enter

   ```
   https://github.com/RCRaceHub/EventScreenDisplay/releases/latest/download/eventscreen-imager.json
   ```

   (or start Imager with `rpi-imager --repo <that URL>`). This always points
   at the latest release. Switch back to the official repository the same
   way when you want Raspberry Pi OS again.

Then for each card:

1. Choose your Pi model, then **EventScreen** as the operating system, then
   the SD card.
2. In the customisation step set:
   - **Hostname**: the screen's name, for example `screen-01`. Use a
     different name for every screen; it's how you reach it
     (`screen-01.local`).
   - **Username and password** for yourself, for SSH.
   - **Wi-Fi** network, password and country.
   - **Enable SSH**.
3. Write the card.

Imager downloads the image once and keeps it, so the next cards are quicker.

### From a downloaded file

Download `eventscreen-<version>-arm64.img.xz` from the
[releases page](https://github.com/RCRaceHub/EventScreenDisplay/releases/latest)
and pick it in Imager with **Use custom**. Imager may not offer its settings
for a file it doesn't know; if it doesn't, use the listing above. Any other
SD card writer (balenaEtcher, `dd`) also writes the image, but without the
hostname and Wi-Fi settings the Pi can't get on the network by itself; plug
in Ethernet, or let it start its setup hotspot (see
[Changing the Wi-Fi network](installation.md#changing-the-wi-fi-network)).

## Per-screen settings: eventscreen.txt

After writing, the card's boot partition (the drive called `bootfs` that
Windows and macOS open when the card is plugged in) holds
`eventscreen.txt.example`. To give a screen its own settings, copy it to
`eventscreen.txt` in the same place, remove the `#` from the settings you
want and change them:

```
name=Pit lane board
token=a-long-secret-for-the-api
```

| Setting | Meaning |
| --- | --- |
| `name` | Friendly name on the idle screen and in the API. Defaults to the hostname. Up to 64 characters, no `"`, `\`, `\|` or `&` |
| `token` | Require `Authorization: Bearer <token>` for any change over the API. 8 or more letters, digits or `._~+/=-` |
| `port` | API port. Default 8080 |
| `resolution` | HDMI mode. Default `1920x1080@60D`; `auto` uses what the TV prefers; add rotation like `1920x1080@60D,rotate=90` |

The screen applies the file the next time it starts, before the viewer
starts, then removes it from the card (a copy only root can read is kept at
`/var/lib/eventscreen/eventscreen.txt.applied`), so the token doesn't stay
on a card anyone can read. If `resolution` changed, the Pi reboots once by
itself. Settings you leave out keep their current value, so you can drop a
new `eventscreen.txt` with just one line on the card later to change it.

If the file has a mistake, nothing in it is applied: it's renamed
`eventscreen.txt.error` with the reason on its first line. Fix it, rename it
back to `eventscreen.txt` and start the Pi again.

Hostname, Wi-Fi and SSH aren't in this file: set them in Imager.

## First boot

The first boot takes a few minutes and restarts once while Raspberry Pi OS
grows the root partition to fill the card and applies the Imager settings.
Then the TV shows the idle screen with the screen's name and API address,
ready for a playlist (see the [user guide](user-guide.md)). From then on
the Pi behaves exactly like one set up by hand; see
[Running unattended](installation.md#running-unattended).

`sudo eventscreen-check` over SSH checks everything, as for any screen.

## Updating a screen made from the image

The same as any screen: install the new `.deb` over the top (see
[Updating](installation.md#updating)), or write a new card. Raspberry Pi OS
updates come from `sudo apt update && sudo apt full-upgrade` as usual.

## What's in the image

- The Raspberry Pi OS Lite (64-bit) release named in `image/base-image.env`,
  unchanged apart from what's below. It still sets itself up on first boot
  from Imager's settings (cloud-init) and grows the root partition.
- The `eventscreen` package with its services enabled, `cog`, `libgles2`,
  `libnss-mdns` and `fonts-dejavu-core`, and their dependencies.
- `eventscreen-setup --offline` already run: `video=HDMI-A-1:1920x1080@60D`
  and `consoleblank=0` in `cmdline.txt`. The tty1 login prompt is masked
  (Alt+F2 on a keyboard still gives one; SSH works as normal).
- `eventscreen.txt.example` on the boot partition and
  `/etc/eventscreen-image`, which records the eventscreen version and the
  Raspberry Pi OS image it was built from.

## Building the image yourself

CI builds the image on every pull request (the `SD card image` check, with
the image as the `eventscreen-image` artifact) and attaches it to every
release. To build one on a Linux machine:

```bash
sudo apt install qemu-user-static mtools parted xz-utils
make deb-arm64
source image/base-image.env
curl -fL -o base.img.xz "$BASE_URL"
echo "$BASE_SHA256  base.img.xz" | sha256sum -c
sudo image/build-image.sh base.img.xz dist/eventscreen_*_arm64.deb eventscreen.img
sudo image/check-image.sh eventscreen.img
xz -T0 eventscreen.img
```

It takes about 6 minutes plus compression, and needs internet for the
browser's packages. `build-image.sh` installs the package in a chroot of the
image under qemu, so it needs root, loop devices and qemu's aarch64 binfmt
handler registered with the fix-binary (`F`) flag, which Debian's and
Ubuntu's `qemu-user-static` do. The boot partition is edited with mtools, so
it works in containers without a FAT driver.

To move to a newer Raspberry Pi OS, change `BASE_URL` and `BASE_SHA256` in
`image/base-image.env` and check the `SD card image` check on the pull
request.
