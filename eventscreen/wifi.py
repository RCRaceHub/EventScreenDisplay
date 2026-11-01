"""Wi-Fi fallback hotspot with a setup page.

Runs as root in its own service (eventscreen-wifi.service), separate from
the unprivileged viewer agent, because it drives NetworkManager:

    python3 -m eventscreen.wifi --config /etc/eventscreen/config.toml

When the Pi hasn't been on any network for wifi_fallback_after seconds it
starts a hotspot, puts the hotspot's name and password on the TV (through
the agent's "show" API), and serves a setup page on http://10.42.0.1 where
a phone can pick the venue's Wi-Fi. NetworkManager's DNS for the hotspot
points every name at the Pi (see deploy/captive-dns.conf), so most phones
open the setup page by themselves. Every wifi_retry_interval seconds it
drops the hotspot briefly to retry the known networks, in case the venue
Wi-Fi was just late coming up.
"""

from __future__ import annotations

import argparse
import html
import json
import logging
import secrets
import signal
import socket
import subprocess
import threading
import time
import urllib.request
from dataclasses import dataclass
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from urllib.parse import parse_qs, urlparse

from . import config

log = logging.getLogger(__name__)

HOTSPOT_CON = "eventscreen-hotspot"
# NetworkManager's address for the Pi on a shared (hotspot) connection.
HOTSPOT_ADDR = "10.42.0.1"
IFACE = "wlan0"
SETUP_URL = f"http://{HOTSPOT_ADDR}/"
SCREEN_URL = f"http://{HOTSPOT_ADDR}/screen"
# After dropping the hotspot, how long known networks get to connect.
RETRY_WINDOW = 45
# Don't drop the hotspot for a retry while someone is using the setup page.
ACTIVITY_GRACE = 120
TICK = 10
PASSWORD_ALPHABET = "abcdefghjkmnpqrstuvwxyz23456789"


def split_terse(line: str) -> list[str]:
    """Split one line of `nmcli -t` output, which escapes ':' and '\\'."""
    fields, cur, i = [], [], 0
    while i < len(line):
        c = line[i]
        if c == "\\" and i + 1 < len(line):
            cur.append(line[i + 1])
            i += 2
            continue
        if c == ":":
            fields.append("".join(cur))
            cur = []
        else:
            cur.append(c)
        i += 1
    fields.append("".join(cur))
    return fields


@dataclass
class Network:
    ssid: str
    signal: int
    secure: bool


class NetworkManager:
    """The few nmcli calls the fallback needs."""

    def __init__(self, run=None):
        self._run = run or self._run_nmcli

    @staticmethod
    def _run_nmcli(args: list[str], timeout: float) -> str:
        res = subprocess.run(["nmcli", *args], capture_output=True, text=True, timeout=timeout)
        if res.returncode != 0:
            raise RuntimeError(res.stderr.strip() or f"nmcli {args[0]} failed")
        return res.stdout

    def nmcli(self, *args: str, timeout: float = 30) -> str:
        return self._run(list(args), timeout)

    def devices(self) -> list[dict]:
        out = self.nmcli("-t", "-f", "DEVICE,TYPE,STATE,CONNECTION", "device")
        rows = []
        for line in out.splitlines():
            f = split_terse(line)
            if len(f) >= 4:
                rows.append({"device": f[0], "type": f[1], "state": f[2], "connection": f[3]})
        return rows

    def online(self) -> bool:
        """Connected to a real network: Wi-Fi (not our hotspot) or Ethernet."""
        return any(
            d["type"] in ("wifi", "ethernet") and d["state"] == "connected" and d["connection"] != HOTSPOT_CON
            for d in self.devices()
        )

    def hotspot_active(self) -> bool:
        return any(d["connection"] == HOTSPOT_CON and d["state"] == "connected" for d in self.devices())

    def has_wifi(self) -> bool:
        return any(d["device"] == IFACE for d in self.devices())

    def scan(self) -> list[Network]:
        try:
            out = self.nmcli("-t", "-f", "SSID,SIGNAL,SECURITY", "device", "wifi", "list",
                             "--rescan", "yes", timeout=40)
        except Exception as e:
            log.warning("Wi-Fi scan failed: %s", e)
            return []
        best: dict[str, Network] = {}
        for line in out.splitlines():
            f = split_terse(line)
            if len(f) < 3 or not f[0]:
                continue
            signal = int(f[1]) if f[1].isdigit() else 0
            net = Network(f[0], signal, f[2] not in ("", "--"))
            if net.ssid not in best or signal > best[net.ssid].signal:
                best[net.ssid] = net
        return sorted(best.values(), key=lambda n: -n.signal)

    def start_hotspot(self, ssid: str, password: str) -> None:
        self._delete(HOTSPOT_CON)
        self.nmcli("device", "wifi", "hotspot", "ifname", IFACE, "con-name", HOTSPOT_CON,
                   "ssid", ssid, "password", password, timeout=60)
        # Only ever started by us, never at boot.
        self.nmcli("connection", "modify", HOTSPOT_CON, "connection.autoconnect", "no")

    def stop_hotspot(self) -> None:
        try:
            self.nmcli("connection", "down", HOTSPOT_CON)
        except Exception:
            pass

    def save_network(self, ssid: str, password: str, hidden: bool = False) -> str:
        """Store a Wi-Fi network for autoconnect; returns the connection name."""
        name = f"wifi-{ssid}"
        self._delete(name)
        args = ["connection", "add", "type", "wifi", "con-name", name, "ifname", IFACE,
                "ssid", ssid, "connection.autoconnect", "yes"]
        if password:
            args += ["wifi-sec.key-mgmt", "wpa-psk", "wifi-sec.psk", password]
        if hidden:
            args += ["802-11-wireless.hidden", "yes"]
        self.nmcli(*args)
        return name

    def connect(self, name: str, wait: int = RETRY_WINDOW) -> bool:
        try:
            self.nmcli("--wait", str(wait), "connection", "up", name, timeout=wait + 15)
            return True
        except Exception as e:
            log.warning("couldn't connect to %s: %s", name, e)
            return False

    def _delete(self, name: str) -> None:
        try:
            self.nmcli("connection", "delete", name)
        except Exception:
            pass


class Screen:
    """Puts the setup instructions on the TV through the agent's API."""

    def __init__(self, cfg: config.Config):
        self.base = f"http://127.0.0.1:{cfg.port}/api/v1"
        self.token = cfg.api_token

    def _call(self, method: str, path: str, body: dict | None = None) -> dict:
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if self.token:
            req.add_header("Authorization", f"Bearer {self.token}")
        with urllib.request.urlopen(req, timeout=5) as r:
            return json.loads(r.read())

    def showing(self) -> bool:
        override = self._call("GET", "/status").get("override")
        return bool(override and override.get("url") == SCREEN_URL)

    def show(self) -> None:
        try:
            if not self.showing():
                self._call("POST", "/control/show", {"url": SCREEN_URL, "label": "Wi-Fi setup"})
        except Exception as e:
            log.warning("couldn't put Wi-Fi setup on the screen: %s", e)

    def clear(self) -> None:
        try:
            if self.showing():
                self._call("DELETE", "/control/show")
        except Exception as e:
            log.warning("couldn't clear Wi-Fi setup from the screen: %s", e)


def hotspot_credentials(cfg: config.Config) -> tuple[str, str]:
    ssid = cfg.hotspot_ssid or f"EventScreen-{socket.gethostname()}"
    password = cfg.hotspot_password
    if not password:
        path = cfg.state_dir / "hotspot-password"
        try:
            password = path.read_text().strip()
        except FileNotFoundError:
            password = ""
        if len(password) < 8:
            password = "".join(secrets.choice(PASSWORD_ALPHABET) for _ in range(10))
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(password + "\n")
            path.chmod(0o600)
    if len(password) < 8:
        raise ValueError("hotspot_password must be at least 8 characters")
    return ssid[:32], password


class Fallback:
    """Decides when to start and stop the hotspot. tick() runs every TICK s."""

    def __init__(self, nm: NetworkManager, cfg: config.Config, screen, ssid: str, password: str,
                 clock=time.monotonic):
        self.nm = nm
        self.cfg = cfg
        self.screen = screen
        self.ssid = ssid
        self.password = password
        self.clock = clock
        self.lock = threading.RLock()
        self.state = "client"
        self.last_online = clock()
        self.hotspot_since = 0.0
        self.last_activity = -ACTIVITY_GRACE
        self.networks: list[Network] = []
        self.message = ""
        self.connecting = ""
        self.on_hotspot_up = lambda: None
        self.on_hotspot_down = lambda: None

    def tick(self) -> None:
        with self.lock:
            now = self.clock()
            if self.state == "client":
                if self.nm.online():
                    self.last_online = now
                elif now - self.last_online >= self.cfg.wifi_fallback_after:
                    log.warning("no network for %ds; starting setup hotspot %s",
                                now - self.last_online, self.ssid)
                    self._start_hotspot(now)
            elif self.nm.online():
                # Ethernet was plugged in while in setup mode.
                log.info("network is back; stopping setup hotspot")
                self._stop_hotspot()
                self.last_online = now
            elif not self.nm.hotspot_active():
                log.warning("setup hotspot went down; restarting it")
                self._start_hotspot(now)
            elif (now - self.hotspot_since >= self.cfg.wifi_retry_interval
                  and now - self.last_activity >= ACTIVITY_GRACE):
                log.info("retrying known networks")
                self._stop_hotspot()
                # Give the known networks RETRY_WINDOW seconds before the
                # hotspot comes back.
                self.last_online = now - self.cfg.wifi_fallback_after + RETRY_WINDOW
            else:
                self.screen.show()

    def choose(self, ssid: str, password: str, hidden: bool = False) -> bool:
        """Save a network from the setup page and try to join it."""
        with self.lock:
            self.connecting = ssid
            try:
                name = self.nm.save_network(ssid, password, hidden)
                self._stop_hotspot()
                if self.nm.connect(name):
                    log.info("joined %s", ssid)
                    self.message = ""
                    self.last_online = self.clock()
                    return True
                self.message = (f"Couldn't join {ssid}. Check the password and that the "
                                "network is in range, then try again.")
            except Exception as e:
                log.exception("saving %s failed", ssid)
                self.message = f"Couldn't save {ssid}: {e}"
            finally:
                self.connecting = ""
            self._start_hotspot(self.clock())
            return False

    def activity(self) -> None:
        self.last_activity = self.clock()

    def shutdown(self) -> None:
        with self.lock:
            if self.state == "hotspot":
                self._stop_hotspot()

    def _start_hotspot(self, now: float) -> None:
        self.networks = [n for n in self.nm.scan() if n.ssid != self.ssid]
        self.nm.start_hotspot(self.ssid, self.password)
        self.state = "hotspot"
        self.hotspot_since = now
        self.on_hotspot_up()
        self.screen.show()

    def _stop_hotspot(self) -> None:
        self.on_hotspot_down()
        self.screen.clear()
        self.nm.stop_hotspot()
        self.state = "client"


# --- setup web server -------------------------------------------------------

class SetupHandler(BaseHTTPRequestHandler):
    fallback: Fallback  # set by SetupServer

    def do_GET(self):
        path = urlparse(self.path).path
        if path == "/screen":
            return self._page(screen_page(self.fallback))
        self.fallback.activity()
        if path == "/":
            return self._page(setup_page(self.fallback))
        # Anything else (phones' captive portal checks included) goes to setup.
        self.send_response(HTTPStatus.FOUND)
        self.send_header("Location", SETUP_URL)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_POST(self):
        self.fallback.activity()
        length = min(int(self.headers.get("Content-Length") or 0), 4096)
        form = parse_qs(self.rfile.read(length).decode(errors="replace"))
        ssid = (form.get("other", [""])[0].strip() or form.get("ssid", [""])[0]).strip()
        password = form.get("password", [""])[0]
        hidden = "hidden" in form
        error = validate(ssid, password)
        if error:
            self.fallback.message = error
            return self._page(setup_page(self.fallback))
        # Joining drops the hotspot (and this phone), so answer first.
        threading.Thread(target=self.fallback.choose, args=(ssid, password, hidden), daemon=True).start()
        self._page(joining_page(ssid, self.fallback.ssid))

    def _page(self, body: str):
        data = body.encode()
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(data)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, fmt, *args):
        log.debug("%s - %s", self.address_string(), fmt % args)


class SetupServer:
    def __init__(self, fallback: Fallback, addr: str = HOTSPOT_ADDR, port: int = 80):
        self.fallback = fallback
        self.addr = addr
        self.port = port
        self.httpd: ThreadingHTTPServer | None = None

    def start(self) -> None:
        if self.httpd:
            return
        handler = type("BoundSetupHandler", (SetupHandler,), {"fallback": self.fallback})
        # The hotspot address can take a moment to appear.
        for _ in range(20):
            try:
                self.httpd = ThreadingHTTPServer((self.addr, self.port), handler)
                break
            except OSError:
                time.sleep(0.5)
        else:
            log.error("couldn't listen on %s:%d for the setup page", self.addr, self.port)
            return
        self.httpd.daemon_threads = True
        threading.Thread(target=self.httpd.serve_forever, name="setup-http", daemon=True).start()

    def stop(self) -> None:
        if self.httpd:
            self.httpd.shutdown()
            self.httpd.server_close()
            self.httpd = None


def validate(ssid: str, password: str) -> str:
    if not ssid:
        return "Pick a network or type its name."
    if len(ssid.encode()) > 32:
        return "Network names are at most 32 bytes."
    if password and not 8 <= len(password) <= 63:
        return "Wi-Fi passwords are 8 to 63 characters (leave it empty for an open network)."
    return ""


STYLE = """<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
body{font-family:system-ui,sans-serif;margin:0;padding:16px;max-width:32rem;margin:auto;line-height:1.4;color:#111;background:#fff}
h1{font-size:1.4rem} label{display:block;margin:.6rem 0 .2rem}
input[type=text],input[type=password],select{width:100%;font-size:1rem;padding:.6rem;box-sizing:border-box}
button{font-size:1.1rem;padding:.7rem 1.2rem;margin-top:1rem;width:100%}
.err{background:#fde8e8;color:#7a1010;padding:.6rem;border-radius:4px}
.muted{color:#555;font-size:.9rem}
@media (prefers-color-scheme: dark){body{color:#eee;background:#151515}.err{background:#4a1515;color:#fdd}.muted{color:#aaa}}
</style>"""


def setup_page(fb: Fallback) -> str:
    e = html.escape
    options = "".join(
        f'<option value="{e(n.ssid)}">{e(n.ssid)} ({n.signal}%{"" if n.secure else ", open"})</option>'
        for n in fb.networks
    )
    error = f'<p class="err">{e(fb.message)}</p>' if fb.message else ""
    return f"""<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Screen Wi-Fi setup</title>{STYLE}</head>
<body><h1>Wi-Fi for {e(socket.gethostname())}</h1>{error}
<form method="post">
<label for="ssid">Network</label>
<select id="ssid" name="ssid">{options or '<option value="">(none found, type it below)</option>'}</select>
<label for="other">Or type a network name</label>
<input id="other" name="other" type="text" autocomplete="off" autocapitalize="none">
<label><input type="checkbox" name="hidden"> Hidden network</label>
<label for="password">Password</label>
<input id="password" name="password" type="password" autocomplete="off">
<button type="submit">Connect</button>
</form>
<p class="muted">The screen leaves this setup hotspot to try the network. If it can't connect,
the hotspot comes back in about a minute so you can try again.</p></body></html>"""


def joining_page(ssid: str, hotspot: str) -> str:
    e = html.escape
    return f"""<!doctype html><html lang="en"><head><meta charset="utf-8"><title>Connecting</title>{STYLE}</head>
<body><h1>Connecting to {e(ssid)}</h1>
<p>Your phone will drop off this hotspot now. If the screen goes back to its pages, it worked.</p>
<p>If it can't connect, <b>{e(hotspot)}</b> comes back within about a minute: join it again to retry.</p>
</body></html>"""


def screen_page(fb: Fallback) -> str:
    """Shown full-screen on the TV while in setup mode."""
    e = html.escape
    status = (f"Connecting to {e(fb.connecting)}…" if fb.connecting
              else e(fb.message) if fb.message else "No known Wi-Fi network found.")
    return f"""<!doctype html><html lang="en"><head><meta charset="utf-8"><meta http-equiv="refresh" content="5">
<title>Wi-Fi setup</title><style>
html,body{{margin:0;height:100%;background:#111;color:#eee;font-family:sans-serif}}
body{{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:1.2vw;text-align:center}}
h1{{font-size:4.5vw;margin:0 0 1vw}} p{{font-size:2.4vw;margin:0;color:#bbb}} b{{color:#fff;font-family:monospace}}
</style></head><body>
<h1>Wi-Fi setup for {e(socket.gethostname())}</h1>
<p>{status}</p>
<p>1. On a phone, join Wi-Fi <b>{e(fb.ssid)}</b> password <b>{e(fb.password)}</b></p>
<p>2. The setup page opens by itself, or go to <b>{SETUP_URL}</b></p>
<p style="font-size:1.6vw">Retrying known networks every few minutes.</p>
</body></html>"""


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="eventscreen.wifi")
    parser.add_argument("--config", type=Path, default=config.DEFAULT_CONFIG_PATH)
    parser.add_argument("-v", "--verbose", action="store_true")
    args = parser.parse_args(argv)
    logging.basicConfig(level=logging.DEBUG if args.verbose else logging.INFO,
                        format="%(levelname)s %(name)s: %(message)s")

    cfg = config.load(args.config)
    if not cfg.wifi_fallback:
        log.info("wifi_fallback is off in %s; nothing to do", args.config)
        return 0
    nm = NetworkManager()
    if not nm.has_wifi():
        log.info("no %s on this machine; nothing to do", IFACE)
        return 0
    ssid, password = hotspot_credentials(cfg)
    fallback = Fallback(nm, cfg, Screen(cfg), ssid, password)
    server = SetupServer(fallback)
    fallback.on_hotspot_up = server.start
    fallback.on_hotspot_down = server.stop
    log.info("watching Wi-Fi; setup hotspot %s after %ds offline", ssid, cfg.wifi_fallback_after)

    stopping = threading.Event()
    signal.signal(signal.SIGTERM, lambda *_: stopping.set())
    signal.signal(signal.SIGINT, lambda *_: stopping.set())
    try:
        while not stopping.is_set():
            try:
                fallback.tick()
            except Exception:
                log.exception("Wi-Fi check failed")
            stopping.wait(TICK)
    finally:
        fallback.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
