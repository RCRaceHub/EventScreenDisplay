"""/api/v1/wifi end to end: agent API -> Unix socket -> Wi-Fi service -> fake NetworkManager."""

import json
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from eventscreen import wifi
from eventscreen.api import Agent, WifiClient, make_server
from eventscreen.browsers import DummyBrowser
from eventscreen.config import Config
from eventscreen.player import Player
from eventscreen.playlist import Store

from .test_wifi import Clock, FakeScreen

TOKEN = "s3cret"


class FakeNM:
    """Saved networks and the current one, as NetworkManager would report them."""

    def __init__(self):
        self.networks = {"Home": {"priority": 0, "password": "homepass1", "hidden": False}}
        self.active = "Home"
        self.join_ok = True
        self.hotspot = False

    def online(self):
        return self.active is not None

    def hotspot_active(self):
        return self.hotspot

    def scan(self):
        return [wifi.Network("Venue", 70, True), wifi.Network("Home", 60, True),
                wifi.Network("EventScreen-test", 99, True)]

    def start_hotspot(self, ssid, password):
        self.hotspot = True

    def stop_hotspot(self):
        self.hotspot = False

    def save_network(self, ssid, password, hidden=False, priority=0):
        self.networks[ssid] = {"priority": priority, "password": password, "hidden": hidden}
        return f"wifi-{ssid}"

    def connect(self, name, wait=0):
        if self.join_ok:
            self.active = name.removeprefix("wifi-")
        return self.join_ok

    def saved(self):
        return sorted(({"ssid": s, "connection": f"wifi-{s}", "priority": n["priority"],
                        "active": s == self.active} for s, n in self.networks.items()),
                      key=lambda n: (-n["priority"], n["ssid"]))

    def current(self):
        if self.active is None:
            return None
        return {"connection": f"wifi-{self.active}", "ssid": self.active, "signal": 60, "ip": "192.168.1.50"}

    def forget(self, ssid):
        return 1 if self.networks.pop(ssid, None) else 0


class WifiApiTest(unittest.TestCase):
    token = TOKEN

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        tmp = Path(self.tmp.name)
        self.cfg = Config(screen_id="screen-test", port=0, listen="127.0.0.1", state_dir=tmp,
                          browser="dummy", api_token=self.token)
        self.nm = FakeNM()
        self.fallback = wifi.Fallback(self.nm, self.cfg, FakeScreen(), "EventScreen-test", "secret123",
                                      clock=Clock())
        self.control = wifi.WifiControl(self.nm, self.fallback, clock=lambda: 1234.0)
        sock = tmp / "control.sock"
        self.control_server = wifi.ControlServer(self.control, path=sock)
        self.control_server.start()

        self.player = Player(DummyBrowser(), Store(self.cfg.playlist_path), idle_url="http://127.0.0.1/idle",
                             probe=lambda url: True)
        self.server = make_server(Agent(self.cfg, self.player, wifi=WifiClient(sock, timeout=5)))
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}/api/v1"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.player.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.player.stop()
        self.control_server.stop()
        self.tmp.cleanup()

    def call(self, method, path, body=None, token=TOKEN):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        try:
            with urllib.request.urlopen(req, timeout=10) as r:
                return r.status, json.loads(r.read())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read())

    def wait_for(self, predicate, timeout=5):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if predicate():
                return
            time.sleep(0.02)
        self.fail("condition not met in time")


class TestWifiApi(WifiApiTest):
    def test_status(self):
        status, body = self.call("GET", "/wifi", token=None)  # reads are open
        self.assertEqual(status, 200)
        self.assertEqual(body["mode"], "client")
        self.assertEqual(body["connected"]["ssid"], "Home")
        self.assertEqual(body["saved"], [{"ssid": "Home", "priority": 0, "active": True}])
        self.assertEqual(body["hotspot"]["ssid"], "EventScreen-test")
        # Passwords never come back.
        self.assertNotIn("homepass1", json.dumps(body))
        self.assertNotIn("secret123", json.dumps(body))

    def test_scan_hides_own_hotspot(self):
        status, body = self.call("GET", "/wifi/scan")
        self.assertEqual(status, 200)
        self.assertEqual([n["ssid"] for n in body["networks"]], ["Venue", "Home"])

    def test_add_without_connecting(self):
        status, body = self.call("POST", "/wifi/networks", {"ssid": "Venue", "password": "venuepass", "priority": 10})
        self.assertEqual(status, 201)
        self.assertEqual(body, {"ssid": "Venue", "saved": True, "connecting": False})
        self.assertEqual(self.nm.networks["Venue"]["priority"], 10)
        self.assertEqual(self.nm.active, "Home")
        _, st = self.call("GET", "/wifi")
        self.assertEqual([n["ssid"] for n in st["saved"]], ["Venue", "Home"])

    def test_add_and_connect(self):
        status, body = self.call("POST", "/wifi/networks",
                                 {"ssid": "Venue", "password": "venuepass", "connect": True})
        self.assertEqual(status, 202)
        self.assertTrue(body["connecting"])
        self.wait_for(lambda: self.control.last_change is not None)
        self.assertEqual(self.nm.active, "Venue")
        _, st = self.call("GET", "/wifi")
        self.assertEqual(st["last_change"], {"ssid": "Venue", "ok": True, "message": "", "at": 1234.0})

    def test_connect_failure_is_reported(self):
        self.nm.join_ok = False
        self.call("POST", "/wifi/networks", {"ssid": "Venue", "password": "wrongpass", "connect": True})
        self.wait_for(lambda: self.control.last_change is not None)
        _, st = self.call("GET", "/wifi")
        self.assertFalse(st["last_change"]["ok"])
        self.assertIn("Venue", st["last_change"]["message"])

    def test_connect_from_setup_mode(self):
        self.nm.active = None
        self.fallback.clock.t += 120
        self.fallback.tick()
        self.assertEqual(self.fallback.state, "hotspot")
        _, st = self.call("GET", "/wifi")
        self.assertEqual(st["mode"], "hotspot")
        self.call("POST", "/wifi/networks", {"ssid": "Venue", "password": "venuepass", "connect": True})
        self.wait_for(lambda: self.control.last_change is not None)
        self.assertEqual(self.fallback.state, "client")
        self.assertFalse(self.nm.hotspot)

    def test_validation(self):
        bad = [
            {"password": "venuepass"},
            {"ssid": "Venue", "password": "short"},
            {"ssid": "x" * 33},
            {"ssid": "Venue", "priority": "high"},
            {"ssid": "Venue", "connect": "yes"},
            {"ssid": "EventScreen-test", "password": "whatever1"},
        ]
        for body in bad:
            with self.subTest(body=body):
                status, resp = self.call("POST", "/wifi/networks", body)
                self.assertEqual(status, 400)
                self.assertIn("error", resp)
        self.assertEqual(list(self.nm.networks), ["Home"])

    def test_forget(self):
        self.call("POST", "/wifi/networks", {"ssid": "Old Venue/2", "password": "venuepass"})
        status, body = self.call("DELETE", "/wifi/networks/Old%20Venue%2F2")
        self.assertEqual(status, 200)
        self.assertEqual(body, {"ssid": "Old Venue/2", "forgotten": True})
        self.assertNotIn("Old Venue/2", self.nm.networks)
        status, _ = self.call("DELETE", "/wifi/networks/Nowhere")
        self.assertEqual(status, 404)

    def test_cant_forget_current_network(self):
        status, body = self.call("DELETE", "/wifi/networks/Home")
        self.assertEqual(status, 409)
        self.assertIn("Home", self.nm.networks)

    def test_writes_need_token(self):
        status, _ = self.call("POST", "/wifi/networks", {"ssid": "Venue", "password": "venuepass"}, token=None)
        self.assertEqual(status, 401)
        status, _ = self.call("DELETE", "/wifi/networks/Home", token="wrong")
        self.assertEqual(status, 401)

    def test_service_down(self):
        self.control_server.stop()
        status, body = self.call("GET", "/wifi")
        self.assertEqual(status, 503)
        self.assertIn("eventscreen-wifi", body["error"])


class TestWifiApiWithoutToken(WifiApiTest):
    token = ""

    def test_reads_work(self):
        status, _ = self.call("GET", "/wifi", token=None)
        self.assertEqual(status, 200)

    def test_writes_refused(self):
        status, body = self.call("POST", "/wifi/networks", {"ssid": "Venue", "password": "venuepass"}, token=None)
        self.assertEqual(status, 403)
        self.assertIn("api_token", body["error"])
        self.assertNotIn("Venue", self.nm.networks)


if __name__ == "__main__":
    unittest.main()
