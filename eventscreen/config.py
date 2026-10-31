"""Agent configuration, loaded from a TOML file with sensible defaults."""

from __future__ import annotations

import socket
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

DEFAULT_CONFIG_PATH = Path("/etc/eventscreen/config.toml")


@dataclass
class Config:
    # Stable identifier reported to the manager. Defaults to the hostname.
    screen_id: str = field(default_factory=socket.gethostname)
    # Human-friendly name, e.g. "Pit lane board".
    name: str = ""
    # HTTP API bind address and port.
    listen: str = "0.0.0.0"
    port: int = 8080
    # If set, write requests must send "Authorization: Bearer <token>".
    api_token: str = ""
    # Where the current playlist is persisted so it survives reboots.
    state_dir: Path = Path("/var/lib/eventscreen")
    # Browser driver: "cog" (WPE WebKit), "chromium" or "dummy" (no display).
    browser: str = "cog"
    # Extra arguments passed to the browser binary.
    browser_args: list[str] = field(default_factory=list)
    # Dwell time used when a playlist item doesn't give one.
    default_duration: int = 30
    # Hard floor so a bad playlist can't thrash the Pi.
    min_duration: int = 5

    @property
    def playlist_path(self) -> Path:
        return self.state_dir / "playlist.json"


def load(path: Path | None = None) -> Config:
    path = path or DEFAULT_CONFIG_PATH
    cfg = Config()
    if not path.exists():
        return cfg
    with path.open("rb") as f:
        data = tomllib.load(f)
    for key, value in data.items():
        if not hasattr(cfg, key):
            raise ValueError(f"unknown config key {key!r} in {path}")
        if key == "state_dir":
            value = Path(value)
        setattr(cfg, key, value)
    return cfg
