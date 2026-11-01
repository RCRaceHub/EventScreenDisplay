"""Preloading: smooth page switches without a blank gap.

Instead of navigating the kiosk browser to each page, the browser stays on
one local "stage" page (served by the agent at /stage) holding two
full-screen iframes. The page that's showing lives in one; shortly before
a switch the player asks for the next page to be loaded in the other,
hidden one, and at switch time the stage just swaps which frame is
visible. Only two pages are ever loaded, so memory stays bounded on a
Pi Zero 2 W, and the old frame is blanked once the swap is done.

Pages that refuse to be framed (X-Frame-Options, or a CSP frame-ancestors
rule) are shown the old way, by navigating the browser to them directly.

The stage page learns what to show by long-polling /stage/state.
"""

from __future__ import annotations

import logging
import threading
import time
import urllib.error
import urllib.request

from .browsers import Browser

log = logging.getLogger(__name__)

# How long a framing check result is trusted.
FRAMABLE_TTL = 600.0


def framable(url: str, timeout: float = 4.0) -> bool | None:
    """Can this page be shown in an iframe? None if it couldn't be checked."""
    req = urllib.request.Request(url, method="GET", headers={"User-Agent": "eventscreen-framecheck"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            headers = r.headers
    except urllib.error.HTTPError as e:
        headers = e.headers
    except Exception:
        return None
    xfo = (headers.get("X-Frame-Options") or "").strip().lower()
    if xfo in ("deny", "sameorigin") or xfo.startswith("allow-from"):
        return False
    for csp in headers.get_all("Content-Security-Policy") or []:
        for directive in csp.split(";"):
            parts = directive.split()
            if parts and parts[0].lower() == "frame-ancestors":
                sources = [p.strip("'").lower() for p in parts[1:]]
                if "*" not in sources:
                    return False
    return True


class Stage:
    """What the stage page should show. Read by the page via long-poll."""

    def __init__(self, messages=None):
        self._cond = threading.Condition()
        self.seq = 0
        self.active: str | None = None
        self.next: str | None = None
        self.reloads = 0
        # The banner (messages.Messages) is drawn over the frames.
        self.messages = messages
        if messages is not None:
            messages.on_change(self._changed)

    def _changed(self) -> None:
        with self._cond:
            self._bump()

    def _bump(self) -> None:
        self.seq += 1
        self._cond.notify_all()

    def show(self, url: str) -> None:
        with self._cond:
            self.active = url
            if self.next == url:
                self.next = None
            self._bump()

    def preload(self, url: str | None) -> None:
        with self._cond:
            if url == self.active:
                url = None
            if url != self.next:
                self.next = url
                self._bump()

    def reload(self) -> None:
        with self._cond:
            self.reloads += 1
            self._bump()

    def state(self) -> dict:
        banner = self.messages.banner if self.messages is not None else None
        return {"seq": self.seq, "active": self.active, "next": self.next, "reloads": self.reloads,
                "banner": banner, "now": time.time()}

    def wait(self, since: int, timeout: float = 25.0) -> dict:
        """Return the state once seq has moved past `since`, or after timeout."""
        deadline = time.monotonic() + timeout
        with self._cond:
            while self.seq <= since:
                left = deadline - time.monotonic()
                if left <= 0:
                    break
                self._cond.wait(left)
            return self.state()


class StageBrowser:
    """Wraps a kiosk Browser so pages are shown on the stage when they can be.

    The Player uses it exactly like a Browser, plus preload().
    """

    name = "stage"

    def __init__(self, browser: Browser, stage: Stage, stage_url: str, check=framable,
                 clock=time.monotonic):
        self.browser = browser
        self.stage = stage
        self.stage_url = stage_url
        self.check = check
        self.clock = clock
        self.current_url: str | None = None
        self._cache: dict[str, tuple[bool, float]] = {}

    # Mirror the attributes the player and API read from a Browser.
    @property
    def last_error(self) -> str:
        return self.browser.last_error

    @last_error.setter
    def last_error(self, value: str) -> None:
        self.browser.last_error = value

    @property
    def proc(self):
        return self.browser.proc

    def framable(self, url: str) -> bool:
        if url.startswith(self.stage_url.rsplit("/", 1)[0] + "/"):
            return True  # our own pages (idle)
        cached = self._cache.get(url)
        now = self.clock()
        if cached and now - cached[1] < FRAMABLE_TTL:
            return cached[0]
        ok = self.check(url)
        if ok is None:
            # Unreachable right now: frame it, and check again next time.
            return True
        if not ok:
            log.info("%s refuses to be framed; showing it directly", url)
        self._cache[url] = (ok, now)
        return ok

    def on_stage(self) -> bool:
        return self.browser.current_url == self.stage_url

    def open(self, url: str) -> None:
        if self.framable(url):
            self.stage.show(url)
            if not self.on_stage() or not self.browser.alive():
                self.browser.open(self.stage_url)
        else:
            self.stage.preload(None)
            self.browser.open(url)
        self.current_url = url

    def preload(self, url: str | None) -> None:
        if url and self.on_stage() and self.framable(url):
            self.stage.preload(url)

    def reload(self) -> None:
        if self.on_stage():
            self.stage.reload()
        else:
            self.browser.reload()

    def ensure_running(self) -> None:
        self.browser.ensure_running()

    def alive(self) -> bool:
        return self.browser.alive()

    def stop(self) -> None:
        self.browser.stop()

    def status(self) -> dict:
        st = self.browser.status()
        st["url"] = self.current_url
        st["preload"] = self.on_stage()
        return st


STAGE_PAGE = """<!doctype html>
<html><head><meta charset="utf-8"><title>eventscreen</title>
<style>
html,body{margin:0;height:100%;overflow:hidden;background:#000}
iframe{position:absolute;inset:0;width:100%;height:100%;border:0;background:#000;visibility:hidden}
iframe.on{visibility:visible}
#banner{position:absolute;left:0;right:0;bottom:0;z-index:10;display:none;overflow:hidden;white-space:nowrap;
  font-family:"DejaVu Sans",sans-serif;font-weight:700;font-size:4.2vh;line-height:9vh;height:9vh;
  color:#fff;background:rgba(10,40,80,.94);border-top:.5vh solid #4aa3ff;box-shadow:0 -1vh 3vh rgba(0,0,0,.5)}
#banner.warning{background:rgba(90,60,0,.95);border-top-color:#ffc107}
#banner.urgent{background:rgba(120,10,10,.96);border-top-color:#ff4d4d}
#banner span{display:inline-block;padding:0 3vw}
#banner.scroll span{padding-left:100%;animation:scroll linear infinite}
@keyframes scroll{from{transform:translateX(0)}to{transform:translateX(-100%)}}
</style></head><body>
<div id="banner"><span></span></div>
<iframe id="a" allow="autoplay; fullscreen"></iframe><iframe id="b" allow="autoplay; fullscreen"></iframe>
<script>
// Two frames: one showing, one loading the next page out of sight.
var frames = [document.getElementById("a"), document.getElementById("b")];
var shown = 0, seq = -1, reloads = null, swapTimer = null;
function url(f) { return f.dataset.url || ""; }
function load(f, u) { f.dataset.url = u; f.dataset.ready = ""; f.src = u; }
frames.forEach(function (f) { f.addEventListener("load", function () { f.dataset.ready = "1"; }); });
function swapTo(i) {
  clearTimeout(swapTimer); swapTimer = null;
  if (i === shown && frames[i].className === "on") return;
  var old = frames[shown];
  frames[i].className = "on"; old.className = ""; shown = i;
  // Free the old page's memory once it's out of sight.
  setTimeout(function () { if (old !== frames[shown] && !old.dataset.keep) load(old, "about:blank"); }, 500);
}
function apply(s) {
  var other = 1 - shown;
  if (s.active && url(frames[shown]) !== s.active) {
    if (url(frames[other]) === s.active) {
      // Preloaded: swap now if it has loaded, else as soon as it does (max 10 s).
      if (frames[other].dataset.ready) swapTo(other);
      else if (!swapTimer) {
        var f = frames[other];
        f.addEventListener("load", function h() { f.removeEventListener("load", h); if (url(f) === s.active) swapTo(other); });
        swapTimer = setTimeout(function () { swapTo(other); }, 10000);
      }
    } else {
      // Not preloaded: load it behind the current page, then swap.
      load(frames[other], s.active);
      var g = frames[other];
      g.addEventListener("load", function h() { g.removeEventListener("load", h); if (url(g) === s.active) swapTo(other); });
      swapTimer = setTimeout(function () { swapTo(other); }, 10000);
    }
  } else if (s.active && frames[shown].className !== "on") {
    frames[shown].className = "on";
  }
  other = 1 - shown;
  if (s.next && url(frames[shown]) === s.active && url(frames[other]) !== s.next) load(frames[other], s.next);
  frames[other].dataset.keep = s.next ? "1" : "";
  if (reloads !== null && s.reloads !== reloads && url(frames[shown])) load(frames[shown], url(frames[shown]));
  reloads = s.reloads;
  showBanner(s.banner, s.now);
}
var bannerTimer = null, bannerKey = "";
function showBanner(b, now) {
  var el = document.getElementById("banner"), span = el.firstChild;
  clearTimeout(bannerTimer);
  if (!b) { el.style.display = "none"; bannerKey = ""; return; }
  var key = b.text + "|" + b.level;
  if (key !== bannerKey) {
    bannerKey = key;
    span.textContent = b.text;
    el.className = b.level;
    el.style.display = "block";
    span.style.animationDuration = "";
    // Too long to fit: scroll it, at a steady reading speed.
    if (span.scrollWidth > el.clientWidth) {
      el.className = b.level + " scroll";
      span.style.animationDuration = Math.max(8, (span.scrollWidth + el.clientWidth) / (el.clientWidth / 9)) + "s";
    }
  }
  el.style.display = "block";
  // Hide it on time even if the agent doesn't say so.
  if (b.until) bannerTimer = setTimeout(function () { el.style.display = "none"; bannerKey = ""; },
                                        Math.max(0, (b.until - now) * 1000));
}
function poll() {
  var x = new XMLHttpRequest();
  x.open("GET", "/stage/state?since=" + seq);
  x.timeout = 35000;
  x.onload = function () {
    try { var s = JSON.parse(x.responseText); seq = s.seq; apply(s); } catch (e) {}
    setTimeout(poll, 50);
  };
  x.onerror = x.ontimeout = function () { setTimeout(poll, 2000); };
  x.send();
}
poll();
</script></body></html>
"""
