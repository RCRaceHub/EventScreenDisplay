"""End-to-end tests against the real HTTP server with the dummy browser.

Run with:  python3 -m unittest discover -s tests
"""

import json
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request
from pathlib import Path

from eventscreen import player as player_mod
from eventscreen.api import Agent, make_server
from eventscreen.browsers import DummyBrowser
from eventscreen.config import Config
from eventscreen.player import Player
from eventscreen.playlist import Store


class AgentTest(unittest.TestCase):
    token = ""

    def setUp(self):
        player_mod.HEALTH_INTERVAL = 0.05
        self.tmp = tempfile.TemporaryDirectory()
        self.cfg = Config(screen_id="screen-test", port=0, listen="127.0.0.1",
                          state_dir=Path(self.tmp.name), browser="dummy",
                          min_duration=1, default_duration=1, api_token=self.token)
        self.browser = DummyBrowser()
        self.player = Player(self.browser, Store(self.cfg.playlist_path), idle_url="http://127.0.0.1/idle")
        self.server = make_server(Agent(self.cfg, self.player))
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.player.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.player.stop()
        self.tmp.cleanup()

    def call(self, method, path, body=None, headers=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers or {})
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=5) as r:
                return r.status, json.loads(r.read()), r.headers
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read()), e.headers

    def wait_for(self, predicate, timeout=5):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if predicate():
                return
            time.sleep(0.02)
        self.fail("condition not met in time")


class TestApi(AgentTest):
    def test_info(self):
        status, body, _ = self.call("GET", "/api/v1/info")
        self.assertEqual(status, 200)
        self.assertEqual(body["id"], "screen-test")
        self.assertEqual(body["api_version"], "1")
        self.assertFalse(body["auth_required"])

    def test_idle_without_playlist(self):
        self.wait_for(lambda: self.browser.current_url == "http://127.0.0.1/idle")
        _, body, _ = self.call("GET", "/api/v1/status")
        self.assertEqual(body["state"], "idle")
        self.assertTrue(body["browser"]["running"])

    def test_playlist_cycles(self):
        status, body, headers = self.call("PUT", "/api/v1/playlist", {
            "items": ["http://a.example/", {"url": "http://b.example/", "duration": 1, "label": "B"}],
        })
        self.assertEqual(status, 200)
        self.assertEqual(body["revision"], 1)
        self.assertEqual(headers["ETag"], '"1"')
        self.wait_for(lambda: self.browser.current_url == "http://a.example/")
        self.wait_for(lambda: self.browser.current_url == "http://b.example/")
        self.wait_for(lambda: self.browser.current_url == "http://a.example/")

    def test_playlist_persists(self):
        self.call("PUT", "/api/v1/playlist", {"items": ["http://a.example/"], "source": "rctc"})
        saved = Store(self.cfg.playlist_path).load()
        self.assertEqual(saved.items[0].url, "http://a.example/")
        self.assertEqual(saved.source, "rctc")

    def test_validation(self):
        for items in (["ftp://x"], [{"url": "http://a/", "duration": 0}], "nope", [42]):
            status, body, _ = self.call("PUT", "/api/v1/playlist", {"items": items})
            self.assertEqual(status, 400, items)
            self.assertIn("error", body)

    def test_if_match(self):
        self.call("PUT", "/api/v1/playlist", {"items": ["http://a.example/"]})
        status, _, _ = self.call("PUT", "/api/v1/playlist", {"items": ["http://b.example/"]},
                                 headers={"If-Match": '"0"'})
        self.assertEqual(status, 412)
        status, _, _ = self.call("PUT", "/api/v1/playlist", {"items": ["http://b.example/"]},
                                 headers={"If-Match": '"1"'})
        self.assertEqual(status, 200)

    def test_pause_next_goto(self):
        self.call("PUT", "/api/v1/playlist", {"items": ["http://a.example/", "http://b.example/", "http://c.example/"],
                                             "default_duration": 60})
        self.wait_for(lambda: self.browser.current_url == "http://a.example/")
        _, body, _ = self.call("POST", "/api/v1/control/pause")
        self.assertEqual(body["state"], "paused")
        self.call("POST", "/api/v1/control/next")
        self.wait_for(lambda: self.browser.current_url == "http://b.example/")
        self.call("POST", "/api/v1/control/goto", {"index": 2})
        self.wait_for(lambda: self.browser.current_url == "http://c.example/")
        _, body, _ = self.call("GET", "/api/v1/status")
        self.assertEqual(body["index"], 2)
        self.assertIsNone(body["current"]["ends_at"])
        status, _, _ = self.call("POST", "/api/v1/control/goto", {"index": 9})
        self.assertEqual(status, 400)

    def test_show_override_returns(self):
        self.call("PUT", "/api/v1/playlist", {"items": ["http://a.example/"]})
        self.wait_for(lambda: self.browser.current_url == "http://a.example/")
        _, body, _ = self.call("POST", "/api/v1/control/show", {"url": "http://alert.example/", "duration": 1})
        self.wait_for(lambda: self.browser.current_url == "http://alert.example/")
        self.wait_for(lambda: self.browser.current_url == "http://a.example/")

    def test_browser_restarted_after_crash(self):
        self.wait_for(lambda: self.browser.alive())
        self.browser.proc.kill()
        self.wait_for(lambda: self.browser.restarts == 1 and self.browser.alive())

    def test_unknown_route(self):
        status, _, _ = self.call("GET", "/api/v1/nope")
        self.assertEqual(status, 404)
        status, _, _ = self.call("POST", "/api/v1/status")
        self.assertEqual(status, 405)


class TestAuth(AgentTest):
    token = "s3cret"

    def test_writes_need_token(self):
        status, _, _ = self.call("PUT", "/api/v1/playlist", {"items": []})
        self.assertEqual(status, 401)
        status, _, _ = self.call("PUT", "/api/v1/playlist", {"items": []},
                                 headers={"Authorization": "Bearer s3cret"})
        self.assertEqual(status, 200)

    def test_reads_are_open(self):
        status, body, _ = self.call("GET", "/api/v1/info")
        self.assertEqual(status, 200)
        self.assertTrue(body["auth_required"])


if __name__ == "__main__":
    unittest.main()
