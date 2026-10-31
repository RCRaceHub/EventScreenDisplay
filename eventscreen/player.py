"""Playlist player: decides what should be on screen and when to move on."""

from __future__ import annotations

import logging
import threading
import time
from dataclasses import dataclass

from .browsers import Browser
from .playlist import Item, Playlist, Store

log = logging.getLogger(__name__)

# How often the loop wakes up to check the browser is still alive.
HEALTH_INTERVAL = 2.0


@dataclass
class Override:
    item: Item
    until: float | None  # None = until cleared


class Player:
    def __init__(self, browser: Browser, store: Store, idle_url: str):
        self.browser = browser
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

    # --- lifecycle ------------------------------------------------------
    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        with self._cond:
            self._stop = True
            self._cond.notify_all()
        self._thread.join(timeout=15)
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

    def _run(self) -> None:
        while True:
            reload = False
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

            try:
                if target is not None and target.url != self.browser.current_url:
                    log.info("showing %s", target.url)
                    self.browser.open(target.url)
                elif reload:
                    self.browser.reload()
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
                    timeout = max(0.0, min(timeout, self.ends_at - time.time()))
                self._cond.wait(timeout)
