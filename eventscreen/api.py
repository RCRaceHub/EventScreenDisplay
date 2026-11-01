"""Local HTTP API. See docs/API.md for the contract."""

from __future__ import annotations

import hmac
import html
import http.client
import json
import logging
import socket
import time
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import quote, urlparse

from . import API_VERSION, __version__
from .wifi import CONTROL_SOCKET
from .config import Config
from .player import Player
from .playlist import Item, PlaylistError, parse_duration, parse_items, replaced, validate_url

log = logging.getLogger(__name__)

PREFIX = f"/api/v{API_VERSION}"
MAX_BODY = 256 * 1024


class ApiError(Exception):
    def __init__(self, status: HTTPStatus, message: str):
        super().__init__(message)
        self.status = status
        self.message = message


class WifiClient:
    """Forwards /api/v1/wifi calls to the root eventscreen-wifi service."""

    def __init__(self, path: Path = CONTROL_SOCKET, timeout: float = 60):
        self.path = Path(path)
        self.timeout = timeout

    def call(self, method: str, path: str, body: dict | None = None) -> tuple[int, dict]:
        conn = _UnixConnection(str(self.path), self.timeout)
        try:
            data = None if body is None else json.dumps(body).encode()
            conn.request(method, path, body=data, headers={"Content-Type": "application/json"})
            resp = conn.getresponse()
            return resp.status, json.loads(resp.read() or b"{}")
        except (FileNotFoundError, ConnectionRefusedError):
            raise ApiError(HTTPStatus.SERVICE_UNAVAILABLE,
                           "Wi-Fi service isn't running (systemctl status eventscreen-wifi)")
        finally:
            conn.close()


class _UnixConnection(http.client.HTTPConnection):
    def __init__(self, path: str, timeout: float):
        super().__init__("localhost", timeout=timeout)
        self.unix_path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.unix_path)


class Agent:
    """Glue between the HTTP handler and the player."""

    def __init__(self, cfg: Config, player: Player, wifi: WifiClient | None = None):
        self.cfg = cfg
        self.player = player
        self.wifi = wifi or WifiClient()
        self.booted_at = time.time()

    def info(self) -> dict:
        return {
            "id": self.cfg.screen_id,
            "name": self.cfg.name,
            "hostname": socket.gethostname(),
            "version": __version__,
            "api_version": API_VERSION,
            "browser": self.cfg.browser,
            "auth_required": bool(self.cfg.api_token),
            # Lets a manager tell which endpoint groups a screen has.
            "features": ["playlist", "control", "wifi"],
            "limits": {
                "default_duration": self.cfg.default_duration,
                "min_duration": self.cfg.min_duration,
            },
        }

    def status(self) -> dict:
        return {
            "id": self.cfg.screen_id,
            "uptime": round(time.time() - self.booted_at, 1),
            "time": time.time(),
            **self.player.status(),
            "browser": self.player.browser.status(),
        }


class Handler(BaseHTTPRequestHandler):
    server_version = f"eventscreen/{__version__}"
    agent: Agent  # set by make_server

    # --- routing --------------------------------------------------------
    def do_GET(self):
        self._dispatch("GET")

    def do_PUT(self):
        self._dispatch("PUT")

    def do_POST(self):
        self._dispatch("POST")

    def do_DELETE(self):
        self._dispatch("DELETE")

    def do_OPTIONS(self):
        self.send_response(HTTPStatus.NO_CONTENT)
        self._cors()
        self.end_headers()

    def _dispatch(self, method: str) -> None:
        path = urlparse(self.path).path.rstrip("/") or "/"
        routes = {
            ("GET", "/"): self.idle_page,
            ("GET", "/idle"): self.idle_page,
            ("GET", f"{PREFIX}/info"): self.get_info,
            ("GET", f"{PREFIX}/status"): self.get_status,
            ("GET", f"{PREFIX}/playlist"): self.get_playlist,
            ("PUT", f"{PREFIX}/playlist"): self.put_playlist,
            ("DELETE", f"{PREFIX}/playlist"): self.delete_playlist,
            ("POST", f"{PREFIX}/control/next"): self.ctl(lambda b: self.agent.player.next()),
            ("POST", f"{PREFIX}/control/previous"): self.ctl(lambda b: self.agent.player.previous()),
            ("POST", f"{PREFIX}/control/pause"): self.ctl(lambda b: self.agent.player.pause()),
            ("POST", f"{PREFIX}/control/resume"): self.ctl(lambda b: self.agent.player.resume()),
            ("POST", f"{PREFIX}/control/reload"): self.ctl(lambda b: self.agent.player.reload()),
            ("POST", f"{PREFIX}/control/goto"): self.ctl(self._goto),
            ("POST", f"{PREFIX}/control/show"): self.ctl(self._show),
            ("DELETE", f"{PREFIX}/control/show"): self.ctl(lambda b: self.agent.player.clear_override()),
            ("GET", f"{PREFIX}/wifi"): lambda: self._wifi("GET", "/wifi"),
            ("GET", f"{PREFIX}/wifi/scan"): lambda: self._wifi("GET", "/wifi/scan"),
            ("POST", f"{PREFIX}/wifi/networks"): lambda: self._wifi("POST", "/wifi/networks", self._body()),
        }
        networks = f"{PREFIX}/wifi/networks/"
        if method == "DELETE" and path.startswith(networks) and len(path) > len(networks):
            # The SSID stays percent-encoded on its way to the Wi-Fi service.
            raw = urlparse(self.path).path[len(networks):].rstrip("/")
            routes[(method, path)] = lambda: self._wifi("DELETE", "/wifi/networks/" + quote(raw, safe="%"))
        try:
            handler = routes.get((method, path))
            if handler is None:
                if any(p == path for _, p in routes):
                    raise ApiError(HTTPStatus.METHOD_NOT_ALLOWED, "method not allowed")
                raise ApiError(HTTPStatus.NOT_FOUND, "not found")
            if method != "GET":
                self._check_auth()
                if path.startswith(f"{PREFIX}/wifi") and not self.agent.cfg.api_token:
                    # Changing the Wi-Fi can take a screen off the network for
                    # good, so it's never open to anyone on the LAN.
                    raise ApiError(HTTPStatus.FORBIDDEN, "set api_token on the screen to change "
                                   "Wi-Fi over the API (sudo eventscreen-setup --token ...)")
            handler()
        except ApiError as e:
            self._json({"error": e.message}, e.status)
        except Exception:
            log.exception("error handling %s %s", method, self.path)
            self._json({"error": "internal error"}, HTTPStatus.INTERNAL_SERVER_ERROR)

    # --- endpoints ------------------------------------------------------
    def get_info(self):
        self._json(self.agent.info())

    def get_status(self):
        self._json(self.agent.status())

    def get_playlist(self):
        pl = self.agent.player.playlist
        self._json(pl.to_dict(), headers={"ETag": f'"{pl.revision}"'})

    def put_playlist(self):
        body = self._body()
        cfg = self.agent.cfg
        player = self.agent.player
        # Optimistic concurrency: a manager can send If-Match with the
        # revision it last saw so two managers don't silently clobber.
        if_match = self.headers.get("If-Match")
        if if_match and if_match.strip('"') != str(player.playlist.revision):
            raise ApiError(HTTPStatus.PRECONDITION_FAILED, "playlist revision changed")
        try:
            default = parse_duration(body.get("default_duration"), cfg.default_duration, cfg.min_duration)
            items = parse_items(body.get("items"), default, cfg.min_duration)
        except PlaylistError as e:
            raise ApiError(HTTPStatus.BAD_REQUEST, str(e))
        start = body.get("start_index", 0)
        if not isinstance(start, int) or isinstance(start, bool):
            raise ApiError(HTTPStatus.BAD_REQUEST, "start_index must be an integer")
        pl = replaced(player.playlist, items, source=str(body.get("source", "")))
        player.set_playlist(pl, start)
        self._json(pl.to_dict(), headers={"ETag": f'"{pl.revision}"'})

    def delete_playlist(self):
        player = self.agent.player
        pl = replaced(player.playlist, [])
        player.set_playlist(pl)
        self._json(pl.to_dict(), headers={"ETag": f'"{pl.revision}"'})

    def ctl(self, action):
        def run():
            body = self._body() if self.headers.get("Content-Length") not in (None, "0") else {}
            action(body)
            self._json(self.agent.status())
        return run

    def _goto(self, body: dict):
        index = body.get("index")
        if not isinstance(index, int) or isinstance(index, bool):
            raise ApiError(HTTPStatus.BAD_REQUEST, "index must be an integer")
        try:
            self.agent.player.goto(index)
        except IndexError:
            raise ApiError(HTTPStatus.BAD_REQUEST, "index out of range")

    def _show(self, body: dict):
        try:
            url = validate_url(body.get("url"))
            duration = body.get("duration")
            if duration is not None:
                duration = parse_duration(duration, 0, self.agent.cfg.min_duration)
        except PlaylistError as e:
            raise ApiError(HTTPStatus.BAD_REQUEST, str(e))
        self.agent.player.show(Item(url=url, duration=duration or 0, label=str(body.get("label", ""))), duration)

    def _wifi(self, method: str, path: str, body: dict | None = None):
        status, data = self.agent.wifi.call(method, path, body)
        self._json(data, HTTPStatus(status))

    def idle_page(self):
        info = self.agent.info()
        name = html.escape(info["name"] or info["id"])
        host = html.escape(info["hostname"])
        port = self.agent.cfg.port
        body = IDLE_PAGE.format(name=name, host=host, port=port, version=__version__).encode()
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # --- helpers --------------------------------------------------------
    def _check_auth(self):
        token = self.agent.cfg.api_token
        if not token:
            return
        given = self.headers.get("Authorization", "")
        if not hmac.compare_digest(given.encode(), f"Bearer {token}".encode()):
            raise ApiError(HTTPStatus.UNAUTHORIZED, "missing or invalid bearer token")

    def _body(self) -> dict:
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            raise ApiError(HTTPStatus.BAD_REQUEST, "bad Content-Length")
        if length > MAX_BODY:
            raise ApiError(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, "body too large")
        try:
            data = json.loads(self.rfile.read(length) or b"{}")
        except ValueError:
            raise ApiError(HTTPStatus.BAD_REQUEST, "body must be JSON")
        if not isinstance(data, dict):
            raise ApiError(HTTPStatus.BAD_REQUEST, "body must be a JSON object")
        return data

    def _cors(self):
        # Lets a browser-based manager UI talk to screens directly.
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match")
        self.send_header("Access-Control-Expose-Headers", "ETag")

    def _json(self, data, status=HTTPStatus.OK, headers=None):
        body = json.dumps(data).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self._cors()
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        log.debug("%s - %s", self.address_string(), fmt % args)


def make_server(agent: Agent) -> ThreadingHTTPServer:
    handler = type("BoundHandler", (Handler,), {"agent": agent})
    server = ThreadingHTTPServer((agent.cfg.listen, agent.cfg.port), handler)
    server.daemon_threads = True
    return server


# Shown when there is no playlist, so whoever is setting up the screen can
# see which one it is and where to reach it.
IDLE_PAGE = """<!doctype html>
<html><head><meta charset="utf-8"><title>{name}</title>
<style>
html,body{{margin:0;height:100%;background:#111;color:#eee;font-family:sans-serif}}
body{{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1em}}
h1{{font-size:6vw;margin:0}} p{{font-size:2.5vw;margin:0;color:#aaa}} code{{color:#fff}}
</style></head><body>
<h1>{name}</h1>
<p>Waiting for a playlist</p>
<p>API: <code>http://{host}.local:{port}/api/v1</code></p>
<p style="font-size:1.2vw">eventscreen {version}</p>
</body></html>
"""
