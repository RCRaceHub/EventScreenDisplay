"""Kiosk browser drivers.

Each driver owns one full-screen browser process and can point it at a URL.
The player calls ensure_running() regularly, so a crashed browser is
restarted on the page that should be showing.
"""

from __future__ import annotations

import json
import logging
import os
import shutil
import signal
import subprocess
import time
import urllib.parse
import urllib.request

log = logging.getLogger(__name__)

# A browser that dies within this many seconds of starting counts as a
# failed start, and restarts back off up to MAX_BACKOFF seconds so a
# browser that can't start (no display, say) doesn't spin and flood the log.
QUICK_EXIT = 10.0
MAX_BACKOFF = 60.0


class Browser:
    name = "base"

    def __init__(self, extra_args: list[str] | None = None):
        self.extra_args = extra_args or []
        self.proc: subprocess.Popen | None = None
        self.current_url: str | None = None
        self.restarts = 0
        self.last_error = ""
        self._started_at = 0.0
        self._backoff = 0.0
        self._retry_at = 0.0

    # --- subclass hooks -------------------------------------------------
    def command(self, url: str) -> list[str]:
        raise NotImplementedError

    def _navigate(self, url: str) -> None:
        """Navigate the running browser. Raise on failure to force a restart."""
        raise NotImplementedError

    def _reload(self) -> None:
        self._navigate(self.current_url)

    # --- public API -----------------------------------------------------
    def alive(self) -> bool:
        return self.proc is not None and self.proc.poll() is None

    def open(self, url: str) -> None:
        if not self.alive():
            self._start(url)
            return
        try:
            self._navigate(url)
            self.current_url = url
        except subprocess.TimeoutExpired:
            # The browser is alive but slow to answer (busy with a heavy
            # page on a Pi). The command usually still lands, and restarting
            # would flash the text console, so leave it be.
            self.current_url = url
            self.last_error = "browser slow to answer navigate; not restarting"
            log.warning("%s for %s", self.last_error, url)
        except Exception as e:
            self.last_error = f"navigate failed: {e}"
            log.warning("%s; restarting browser", self.last_error)
            self.stop()
            self.restarts += 1
            self._start(url)

    def reload(self) -> None:
        if self.current_url is None:
            return
        if not self.alive():
            self._start(self.current_url)
            return
        try:
            self._reload()
        except Exception as e:
            self.last_error = f"reload failed: {e}"
            log.warning("%s; restarting browser", self.last_error)
            self.stop()
            self.restarts += 1
            self._start(self.current_url)

    def ensure_running(self) -> None:
        if self.current_url is None or self.alive():
            return
        now = time.monotonic()
        if self._retry_at == 0.0:
            code = self.proc.returncode if self.proc else None
            self.last_error = f"browser exited with code {code}"
            if now - self._started_at < QUICK_EXIT:
                self._backoff = min(max(self._backoff * 2, 2.0), MAX_BACKOFF)
            else:
                self._backoff = 0.0
            self._retry_at = now + self._backoff
            log.warning("%s; restarting in %.0fs", self.last_error, self._backoff)
        if now >= self._retry_at:
            self._retry_at = 0.0
            self._start(self.current_url)

    def stop(self) -> None:
        if not self.alive():
            self.proc = None
            return
        # Start in a new session so the whole group (cage + browser) is stopped.
        os.killpg(self.proc.pid, signal.SIGTERM)
        try:
            self.proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            os.killpg(self.proc.pid, signal.SIGKILL)
            self.proc.wait()
        self.proc = None

    def status(self) -> dict:
        return {
            "driver": self.name,
            "running": self.alive(),
            "pid": self.proc.pid if self.alive() else None,
            "url": self.current_url,
            "restarts": self.restarts,
            "last_error": self.last_error,
        }

    def _start(self, url: str) -> None:
        if self.proc is not None:
            self.restarts += 1
        cmd = self.command(url)
        log.info("starting browser: %s", " ".join(cmd))
        self.proc = subprocess.Popen(cmd, start_new_session=True)
        self._started_at = time.monotonic()
        self.current_url = url
        self._after_start()

    def _after_start(self) -> None:
        pass


class CogBrowser(Browser):
    """WPE WebKit via Cog, rendering straight to the display with DRM/KMS.

    The lightest option: no X11 or Wayland compositor is needed. Navigation
    goes over Cog's D-Bus interface using cogctl, so the agent must run with
    a session bus (the systemd unit wraps it in dbus-run-session).
    """

    name = "cog"

    def command(self, url: str) -> list[str]:
        return [_which("cog"), "--platform=drm", *self.extra_args, url]

    def _navigate(self, url: str) -> None:
        self._cogctl("open", url)

    def _reload(self) -> None:
        self._cogctl("reload")

    def _cogctl(self, *args: str, timeout: float = 15) -> None:
        subprocess.run([_which("cogctl"), *args], check=True, timeout=timeout,
                       capture_output=True)

    def _after_start(self) -> None:
        # Wait briefly for Cog to claim its D-Bus name so the next cogctl works.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline and self.alive():
            try:
                self._cogctl("ping", timeout=5)
                return
            except Exception:
                time.sleep(0.5)


class ChromiumBrowser(Browser):
    """Chromium in kiosk mode inside the cage Wayland compositor.

    Heavier than Cog (marginal on a Pi Zero 2 W) but useful as a fallback for
    pages WebKit renders badly. Controlled with the DevTools HTTP endpoints.
    """

    name = "chromium"
    debug_port = 9222

    def command(self, url: str) -> list[str]:
        chromium = shutil.which("chromium-browser") or _which("chromium")
        return [
            _which("cage"), "--", chromium,
            "--kiosk", "--noerrdialogs", "--disable-infobars",
            "--no-first-run", "--disable-translate", "--disable-features=Translate",
            "--check-for-update-interval=31536000", "--overscroll-history-navigation=0",
            f"--remote-debugging-port={self.debug_port}",
            "--remote-allow-origins=http://127.0.0.1",
            *self.extra_args, url,
        ]

    def _devtools(self, path: str, method: str = "GET"):
        req = urllib.request.Request(f"http://127.0.0.1:{self.debug_port}{path}", method=method)
        with urllib.request.urlopen(req, timeout=5) as r:
            body = r.read()
        try:
            return json.loads(body)
        except ValueError:
            return body.decode()

    def _pages(self) -> list[dict]:
        return [t for t in self._devtools("/json/list") if t.get("type") == "page"]

    def _navigate(self, url: str) -> None:
        old = self._pages()
        new = self._devtools("/json/new?" + urllib.parse.quote(url, safe=""), method="PUT")
        self._devtools(f"/json/activate/{new['id']}")
        for page in old:
            self._devtools(f"/json/close/{page['id']}")

    def _after_start(self) -> None:
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline and self.alive():
            try:
                self._devtools("/json/version")
                return
            except Exception:
                time.sleep(1)


class DummyBrowser(Browser):
    """No display at all; just logs. For development and tests."""

    name = "dummy"

    def command(self, url: str) -> list[str]:
        return ["sleep", "infinity"]

    def _navigate(self, url: str) -> None:
        log.info("dummy browser -> %s", url)

    def _reload(self) -> None:
        log.info("dummy browser reload")


DRIVERS = {b.name: b for b in (CogBrowser, ChromiumBrowser, DummyBrowser)}


def create(name: str, extra_args: list[str]) -> Browser:
    try:
        return DRIVERS[name](extra_args)
    except KeyError:
        raise ValueError(f"unknown browser {name!r}, expected one of {sorted(DRIVERS)}") from None


def _which(binary: str) -> str:
    path = shutil.which(binary)
    if not path:
        raise FileNotFoundError(f"{binary} not found; run deploy/install.sh")
    return path
