"""Playlist player: decides what should be on screen and when to move on."""

from __future__ import annotations

import logging
import socket
import threading
import time
from dataclasses import dataclass
from urllib.parse import urlparse

from .browsers import Browser
from .playlist import Item, Playlist, Store

log = logging.getLogger(__name__)

# How often the loop wakes up to check the browser is still alive.
HEALTH_INTERVAL = 2.0
# How often to check the page's server can be reached.
PROBE_INTERVAL = 10.0


def reachable(url: str, timeout: float = 3.0) -> bool:
    """True if a TCP connection to the URL's host and port succeeds."""
    parsed = urlparse(url)
    port = parsed.port or (443 if parsed.scheme == "https" else 80)
    try:
        with socket.create_connection((parsed.hostname, port), timeout=timeout):
            return True
    except OSError:
        return False


@dataclass
class Override:
    item: Item
    until: float | None  # None = until cleared


class Player:
    def __init__(self, browser: Browser, store: Store, idle_url: str, probe=reachable,
                 preload_ahead: float = 10.0):
        self.browser = browser
        # Seconds before a switch to start loading the next page out of sight,
        # if the browser supports it (see stage.StageBrowser).
        self.preload_ahead = preload_ahead
        self._preloaded_for: float | None = None
        self.probe = probe
        self.store = store
        self.idle_url = idle_url
        self.playlist = store.load()
        self.index = 0
        self.paused = False
        self.override: Override | None = None
        self.started_at = 0.0
        self.ends_at: float | None = None
        self._showing: Item | None = None
        self._want_reload = False
        self._dirty = True
        self._stop = False
        self._cond = threading.Condition()
        self._thread = threading.Thread(target=self._run, name="player", daemon=True)
        self._prober = threading.Thread(target=self._watch_network, name="prober", daemon=True)
        # Last reachability result for the page on screen: (url, ok, checked_at).
        self._probe: tuple[str, bool, float] | None = None

    # --- lifecycle ------------------------------------------------------
    def start(self) -> None:
        self._thread.start()
        self._prober.start()

    def stop(self) -> None:
        with self._cond:
            self._stop = True
            self._cond.notify_all()
        self._thread.join(timeout=15)
        self._prober.join(timeout=5)
        self.browser.stop()

    # --- commands (called from API threads) -----------------------------
    def set_playlist(self, playlist: Playlist, start_index: int = 0) -> None:
        self.store.save(playlist)
        with self._cond:
            self.playlist = playlist
            self.index = start_index if 0 <= start_index < len(playlist.items) else 0
            self._kick()

    def next(self) -> None:
        with self._cond:
            self.override = None
            self._step(1)

    def previous(self) -> None:
        with self._cond:
            self.override = None
            self._step(-1)

    def goto(self, index: int) -> None:
        with self._cond:
            if not 0 <= index < len(self.playlist.items):
                raise IndexError(index)
            self.override = None
            self.index = index
            self._kick()

    def pause(self) -> None:
        with self._cond:
            self.paused = True
            self.ends_at = None
            self._cond.notify_all()

    def resume(self) -> None:
        with self._cond:
            if self.paused:
                self.paused = False
                # Give the current page a fresh dwell rather than jumping straight on.
                self._kick()

    def show(self, item: Item, duration: int | None) -> None:
        """Temporarily show a URL, then return to the playlist."""
        with self._cond:
            until = time.time() + duration if duration else None
            self.override = Override(item, until)
            self._kick()

    def clear_override(self) -> None:
        with self._cond:
            if self.override:
                self.override = None
                self._kick()

    def reload(self) -> None:
        with self._cond:
            self._want_reload = True
            self._cond.notify_all()

    def status(self) -> dict:
        with self._cond:
            if self.override:
                state = "override"
            elif not self.playlist.items:
                state = "idle"
            elif self.paused:
                state = "paused"
            else:
                state = "playing"
            showing = self._showing
            probe = self._probe if showing and self._probe and self._probe[0] == showing.url else None
            return {
                "state": state,
                "paused": self.paused,
                "index": self.index if self.playlist.items else None,
                "current": None if showing is None else {
                    "url": showing.url,
                    "label": showing.label,
                    "id": showing.id,
                    "started_at": self.started_at,
                    "ends_at": self.ends_at,
                },
                "override": None if self.override is None else {
                    "url": self.override.item.url,
                    "until": self.override.until,
                },
                "network": {
                    "page_reachable": None if probe is None else probe[1],
                    "checked_at": None if probe is None else probe[2],
                },
                "playlist": {
                    "revision": self.playlist.revision,
                    "length": len(self.playlist.items),
                    "updated_at": self.playlist.updated_at,
                    "source": self.playlist.source,
                },
            }

    # --- internals ------------------------------------------------------
    def _kick(self) -> None:
        self._dirty = True
        self._cond.notify_all()

    def _step(self, delta: int) -> None:
        n = len(self.playlist.items)
        if n:
            self.index = (self.index + delta) % n
        self._kick()

    def _target(self) -> tuple[Item, float | None]:
        """What should be on screen now, and when it should change (or None)."""
        now = time.time()
        if self.override:
            return self.override.item, self.override.until
        items = self.playlist.items
        if not items:
            return Item(url=self.idle_url, duration=0, label="idle"), None
        item = items[self.index % len(items)]
        # A single page never needs to move on; keep its live connection open.
        if self.paused or len(items) == 1:
            return item, None
        return item, now + item.duration

    def _upcoming(self) -> Item | None:
        """What will be shown when the current page's time is up."""
        items = self.playlist.items
        if self.override:
            if self.override.until is None:
                return None
            return items[self.index % len(items)] if items else None
        if len(items) < 2 or self.paused:
            return None
        return items[(self.index + 1) % len(items)]

    def _run(self) -> None:
        can_preload = hasattr(self.browser, "preload")
        while True:
            reload = False
            preload = None
            with self._cond:
                if self._stop:
                    return
                now = time.time()
                if self.ends_at is not None and now >= self.ends_at:
                    if self.override:
                        self.override = None
                    else:
                        self.index = (self.index + 1) % max(len(self.playlist.items), 1)
                    self._dirty = True
                target = None
                if self._dirty:
                    self._dirty = False
                    target, self.ends_at = self._target()
                    self.started_at = now
                    self._showing = target
                if self._want_reload:
                    self._want_reload = False
                    reload = True
                if (can_preload and self.ends_at is not None and self._preloaded_for != self.ends_at
                        and now >= self.ends_at - self.preload_ahead):
                    self._preloaded_for = self.ends_at
                    upcoming = self._upcoming()
                    preload = upcoming.url if upcoming else None

            try:
                if target is not None and target.url != self.browser.current_url:
                    log.info("showing %s", target.url)
                    self.browser.open(target.url)
                elif reload:
                    self.browser.reload()
                if preload:
                    log.debug("preloading %s", preload)
                    self.browser.preload(preload)
                self.browser.ensure_running()
            except Exception as e:
                self.browser.last_error = str(e)
                log.exception("browser error")
                # Back off, then try to show the same page again.
                time.sleep(HEALTH_INTERVAL)
                with self._cond:
                    if target is not None and not self._dirty:
                        self._dirty = True
                        self.ends_at = None

            with self._cond:
                if self._stop or self._dirty or self._want_reload:
                    continue
                timeout = HEALTH_INTERVAL
                if self.ends_at is not None:
                    wake = self.ends_at
                    if can_preload and self._preloaded_for != self.ends_at:
                        wake = self.ends_at - self.preload_ahead
                    timeout = max(0.0, min(timeout, wake - time.time()))
                self._cond.wait(timeout)

    def _watch_network(self) -> None:
        """Reload the page when its server comes back after being unreachable.

        At boot the browser often loads the first page before Wi-Fi is up, and
        a timing server can restart mid-event. Either way the browser is left
        on an error page; a single-page playlist would never recover on its
        own, so watch for the server to come back and reload.
        """
        while True:
            with self._cond:
                if self._stop:
                    return
                url = self.browser.current_url
            if url and url != self.idle_url:
                ok = self.probe(url)
                previous = self._probe
                self._probe = (url, ok, time.time())
                if ok and previous and previous[0] == url and not previous[1]:
                    log.info("%s reachable again; reloading", url)
                    self.reload()
                elif not ok and (not previous or previous[0] != url or previous[1]):
                    log.warning("%s is not reachable", url)
            with self._cond:
                if self._stop:
                    return
                self._cond.wait(PROBE_INTERVAL)
