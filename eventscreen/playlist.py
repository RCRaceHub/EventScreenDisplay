"""Playlist model, validation and persistence."""

from __future__ import annotations

import json
import logging
import os
import time
from dataclasses import asdict, dataclass, field
from pathlib import Path
from urllib.parse import urlparse

log = logging.getLogger(__name__)


class PlaylistError(ValueError):
    pass


@dataclass
class Item:
    url: str
    duration: int
    label: str = ""
    id: str = ""


@dataclass
class Playlist:
    items: list[Item] = field(default_factory=list)
    # Bumped on every change so a manager can detect drift cheaply.
    revision: int = 0
    updated_at: float = 0.0
    # Opaque value the manager can set to tag where this playlist came from.
    source: str = ""

    def to_dict(self) -> dict:
        return {
            "items": [asdict(i) for i in self.items],
            "revision": self.revision,
            "updated_at": self.updated_at,
            "source": self.source,
        }


def validate_url(url: object) -> str:
    if not isinstance(url, str) or not url:
        raise PlaylistError("url must be a non-empty string")
    parsed = urlparse(url)
    if parsed.scheme not in ("http", "https") or not parsed.netloc:
        raise PlaylistError(f"url must be an absolute http(s) URL: {url!r}")
    return url


def parse_duration(value: object, default: int, minimum: int) -> int:
    if value is None:
        return default
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise PlaylistError("duration must be a number of seconds")
    if value < minimum:
        raise PlaylistError(f"duration must be at least {minimum} seconds")
    return int(value)


def parse_items(data: object, default_duration: int, min_duration: int) -> list[Item]:
    """Turn the "items" array of a PUT body into validated Items.

    Each entry may be a plain URL string or an object with url, duration,
    label and id.
    """
    if not isinstance(data, list):
        raise PlaylistError("items must be an array")
    items = []
    for n, raw in enumerate(data):
        try:
            if isinstance(raw, str):
                raw = {"url": raw}
            if not isinstance(raw, dict):
                raise PlaylistError("item must be a URL string or an object")
            items.append(Item(
                url=validate_url(raw.get("url")),
                duration=parse_duration(raw.get("duration"), default_duration, min_duration),
                label=str(raw.get("label", "")),
                id=str(raw.get("id", "")),
            ))
        except PlaylistError as e:
            raise PlaylistError(f"items[{n}]: {e}") from None
    return items


class Store:
    """Loads and atomically saves the playlist to disk."""

    def __init__(self, path: Path):
        self.path = path

    def load(self) -> Playlist:
        try:
            data = json.loads(self.path.read_text())
            return Playlist(
                items=[Item(**i) for i in data.get("items", [])],
                revision=data.get("revision", 0),
                updated_at=data.get("updated_at", 0.0),
                source=data.get("source", ""),
            )
        except FileNotFoundError:
            return Playlist()
        except (ValueError, TypeError, AttributeError) as e:
            # A damaged file (say, from a power cut) mustn't stop the screen
            # starting. Keep it for a look and start with an empty playlist.
            bad = self.path.with_suffix(".bad")
            log.error("can't read %s (%s); moved it to %s and starting empty", self.path, e, bad)
            try:
                os.replace(self.path, bad)
            except OSError:
                pass
            return Playlist()

    def save(self, playlist: Playlist) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(".tmp")
        tmp.write_text(json.dumps(playlist.to_dict(), indent=2))
        os.replace(tmp, self.path)


def replaced(old: Playlist, items: list[Item], source: str = "") -> Playlist:
    return Playlist(items=items, revision=old.revision + 1, updated_at=time.time(), source=source)
