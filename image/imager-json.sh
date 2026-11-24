#!/bin/bash
# Write the Raspberry Pi Imager repository file for a built image, so the
# image can be picked by name in Imager with its settings (hostname, user,
# Wi-Fi, SSH) still on offer. Imager only offers them for images it knows
# how to set up, which this file tells it (init_format cloudinit-rpi, as for
# the Pi OS Lite image it's built from).
#
#   image/imager-json.sh IMAGE.img IMAGE.img.xz URL RELEASE_DATE > eventscreen-imager.json
#
# URL is where IMAGE.img.xz is downloaded from (its GitHub release asset),
# RELEASE_DATE is YYYY-MM-DD. Needs jq.
set -euo pipefail

if [[ $# -ne 4 ]]; then
  sed -n '2,11p' "$0" | sed 's/^# \{0,1\}//' >&2
  exit 1
fi
IMG=$1 XZ=$2 URL=$3 DATE=$4
[[ $DATE =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || { echo "RELEASE_DATE must be YYYY-MM-DD" >&2; exit 1; }
VERSION=$(basename "$XZ" | sed -n 's/^eventscreen-\(.*\)-arm64\.img\.xz$/\1/p')
[[ -n $VERSION ]] || { echo "expected IMAGE.img.xz named eventscreen-VERSION-arm64.img.xz" >&2; exit 1; }

size() { stat -c %s "$1"; }
sha() { sha256sum "$1" | cut -d' ' -f1; }
ICONS=https://downloads.raspberrypi.com/imager/icons

jq -n \
  --arg version "$VERSION" --arg url "$URL" --arg date "$DATE" --arg icons "$ICONS" \
  --argjson extract_size "$(size "$IMG")" --arg extract_sha256 "$(sha "$IMG")" \
  --argjson download_size "$(size "$XZ")" --arg download_sha256 "$(sha "$XZ")" '
{
  imager: {
    devices: [
      {name: "Raspberry Pi Zero 2 W", tags: ["pi3-64bit"], icon: "\($icons)/RPi_Zero_2_W.png",
       description: "The smallest Pi that runs EventScreen", matching_type: "inclusive"},
      {name: "Raspberry Pi 3", tags: ["pi3-64bit"], icon: "\($icons)/RPi_3.png",
       description: "Raspberry Pi 3 Model B and B+", matching_type: "inclusive"},
      {name: "Raspberry Pi 4", tags: ["pi4-64bit"], icon: "\($icons)/RPi_4.png",
       description: "Raspberry Pi 4 Model B and 400", matching_type: "inclusive"},
      {name: "Raspberry Pi 5", tags: ["pi5-64bit"], icon: "\($icons)/RPi_5.png",
       description: "Raspberry Pi 5 and 500", matching_type: "exclusive"}
    ]
  },
  os_list: [
    {
      name: "EventScreen \($version)",
      description: "Raspberry Pi OS Lite (64-bit) set up as an EventScreen display for RCTC timing boards",
      icon: "https://downloads.raspberrypi.com/raspios_armhf/Raspberry_Pi_OS_(32-bit).png",
      url: $url,
      extract_size: $extract_size,
      extract_sha256: $extract_sha256,
      image_download_size: $download_size,
      image_download_sha256: $download_sha256,
      release_date: $date,
      init_format: "cloudinit-rpi",
      devices: ["pi5-64bit", "pi4-64bit", "pi3-64bit"],
      capabilities: []
    }
  ]
}'
