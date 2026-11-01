"""Event messages: full-screen hold/announce screens and a banner.

Two independent slots, each holding at most one message:

- message: takes over the whole screen, above the playlist and any `show`.
  "hold" is for an open-ended wait ("Finals being calculated"), with a
  gentle animation so the screen doesn't look frozen; "announce" is for
  news ("Finals lists are up"). Drawn by the screen itself at /message, so
  it stays up even if the server that sent it goes away.
- banner: a strip across the bottom of whatever is showing. Drawn by the
  stage page, so it needs preload on and a page that allows framing.

Both are saved to the state directory, so they survive a reboot, and expire
by themselves if given a duration.
"""

from __future__ import annotations

import json
import logging
import os
import threading
import time
from pathlib import Path

log = logging.getLogger(__name__)

STYLES = ("hold", "announce")
LEVELS = ("info", "warning", "urgent")
MAX_TITLE = 120
MAX_TEXT = 500


class MessageError(ValueError):
    pass


def _text(body: dict, key: str, limit: int, required: bool) -> str:
    value = body.get(key, "")
    if value is None:
        value = ""
    if not isinstance(value, str):
        raise MessageError(f"{key} must be a string")
    value = value.strip()
    if required and not value:
        raise MessageError(f"{key} is required")
    if len(value) > limit:
        raise MessageError(f"{key} is at most {limit} characters")
    return value


def _choice(body: dict, key: str, options: tuple, default: str) -> str:
    value = body.get(key, default)
    if value not in options:
        raise MessageError(f"{key} must be one of {', '.join(options)}")
    return value


def _duration(body: dict) -> int | None:
    value = body.get("duration")
    if value is None:
        return None
    if isinstance(value, bool) or not isinstance(value, (int, float)) or value < 1:
        raise MessageError("duration must be a number of seconds, at least 1 (leave it out to keep it up)")
    return int(value)


def parse_message(body: dict, now: float) -> dict:
    style = _choice(body, "style", STYLES, "hold")
    title = _text(body, "title", MAX_TITLE, required=True)
    text = _text(body, "text", MAX_TEXT, required=False)
    level = _choice(body, "level", LEVELS, "info")
    duration = _duration(body)
    return {"style": style, "title": title, "text": text, "level": level,
            "since": now, "until": now + duration if duration else None}


def parse_banner(body: dict, now: float) -> dict:
    text = _text(body, "text", MAX_TEXT, required=True)
    level = _choice(body, "level", LEVELS, "info")
    duration = _duration(body)
    return {"text": text, "level": level, "since": now, "until": now + duration if duration else None}


class Messages:
    def __init__(self, path: Path | None = None, clock=time.time):
        self.path = path
        self.clock = clock
        self._lock = threading.Lock()
        self._message: dict | None = None
        self._banner: dict | None = None
        self._listeners: list = []
        self._load()

    def on_change(self, fn) -> None:
        self._listeners.append(fn)

    # --- reading ----------------------------------------------------------
    def _live(self, item: dict | None) -> dict | None:
        if item and item["until"] is not None and item["until"] <= self.clock():
            return None
        return item

    @property
    def message(self) -> dict | None:
        with self._lock:
            return self._live(self._message)

    @property
    def banner(self) -> dict | None:
        with self._lock:
            return self._live(self._banner)

    # --- writing ----------------------------------------------------------
    def set_message(self, body: dict) -> dict:
        msg = parse_message(body, self.clock())
        with self._lock:
            self._message = msg
        self._changed()
        return msg

    def clear_message(self) -> None:
        with self._lock:
            had = self._live(self._message) is not None
            self._message = None
        if had:
            self._changed()

    def set_banner(self, body: dict) -> dict:
        banner = parse_banner(body, self.clock())
        with self._lock:
            self._banner = banner
        self._changed()
        return banner

    def clear_banner(self) -> None:
        with self._lock:
            had = self._live(self._banner) is not None
            self._banner = None
        if had:
            self._changed()

    # --- persistence ------------------------------------------------------
    def _changed(self) -> None:
        self._save()
        for fn in self._listeners:
            try:
                fn()
            except Exception:
                log.exception("message listener failed")

    def _save(self) -> None:
        if not self.path:
            return
        with self._lock:
            data = {"message": self._live(self._message), "banner": self._live(self._banner)}
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            tmp = self.path.with_suffix(".tmp")
            tmp.write_text(json.dumps(data, indent=2))
            os.replace(tmp, self.path)
        except OSError as e:
            log.error("couldn't save messages to %s: %s", self.path, e)

    def _load(self) -> None:
        if not self.path:
            return
        try:
            data = json.loads(self.path.read_text())
            self._message = self._live(data.get("message"))
            self._banner = self._live(data.get("banner"))
        except FileNotFoundError:
            pass
        except (ValueError, TypeError, AttributeError, KeyError) as e:
            log.error("ignoring unreadable %s: %s", self.path, e)
            self._message = self._banner = None


# Shown full-screen while a message is up. It reads the message from the
# agent once a second, so a change of text appears without a reload.
MESSAGE_PAGE = """<!doctype html>
<html><head><meta charset="utf-8"><title>Message</title>
<style>
html,body{margin:0;height:100%;overflow:hidden;font-family:"DejaVu Sans",sans-serif;color:#fff;background:#0b1d33}
body.warning{background:#3a2a00} body.urgent{background:#4a0b0b}
main{height:100%;display:flex;flex-direction:column;align-items:center;justify-content:center;
     gap:3vh;padding:0 6vw;box-sizing:border-box;text-align:center}
h1{font-size:7vw;line-height:1.1;margin:0;font-weight:700}
p{font-size:3.4vw;line-height:1.3;margin:0;color:#d6e2f0;white-space:pre-line}
body.warning p{color:#f6e3b0} body.urgent p{color:#f8cccc}
.dots{display:none;gap:2vw;margin-top:2vh} body.hold .dots{display:flex}
.dots i{width:2.2vw;height:2.2vw;border-radius:50%;background:currentColor;opacity:.25;animation:pulse 1.8s infinite}
.dots i:nth-child(2){animation-delay:.3s} .dots i:nth-child(3){animation-delay:.6s}
@keyframes pulse{0%,100%{opacity:.25}40%{opacity:1}}
body.urgent.announce main{animation:flash 1.6s ease-in-out 3}
@keyframes flash{50%{opacity:.35}}
footer{position:absolute;bottom:3vh;width:100%;text-align:center;font-size:1.6vw;color:rgba(255,255,255,.55)}
</style></head><body class="hold info">
<main><h1 id="title"></h1><p id="text"></p><div class="dots"><i></i><i></i><i></i></div></main>
<footer id="footer"></footer>
<script>
var last = "";
function render(m) {
  var key = JSON.stringify(m);
  if (key === last) return;
  last = key;
  if (!m) { document.getElementById("title").textContent = ""; document.getElementById("text").textContent = ""; return; }
  document.body.className = m.style + " " + m.level;
  document.getElementById("title").textContent = m.title;
  document.getElementById("text").textContent = m.text || "";
}
function tick() {
  var x = new XMLHttpRequest();
  x.open("GET", "/api/v1/message");
  x.onload = function () { try { render(JSON.parse(x.responseText).message); } catch (e) {} };
  x.send();
  var d = new Date();
  document.getElementById("footer").textContent = d.toTimeString().slice(0, 5);
}
tick(); setInterval(tick, 1000);
</script></body></html>
"""
