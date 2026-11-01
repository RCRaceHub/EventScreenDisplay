import json
import tempfile
import threading
import time
import unittest
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

from eventscreen import player as player_mod
from eventscreen.api import Agent, make_server
from eventscreen.browsers import DummyBrowser
from eventscreen.config import Config
from eventscreen.player import Player
from eventscreen.playlist import Item, Playlist, Store, replaced
from eventscreen.stage import Stage, StageBrowser, framable

HEADERS = {
    "/plain": {},
    "/deny": {"X-Frame-Options": "DENY"},
    "/sameorigin": {"X-Frame-Options": "SAMEORIGIN"},
    "/csp-self": {"Content-Security-Policy": "default-src 'self'; frame-ancestors 'self'"},
    "/csp-any": {"Content-Security-Policy": "frame-ancestors *"},
    "/missing": {},
}


class Pages(BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(404 if self.path == "/missing" else 200)
        for k, v in HEADERS.get(self.path, {}).items():
            self.send_header(k, v)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass


class TestFramable(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Pages)
        cls.base = f"http://127.0.0.1:{cls.server.server_address[1]}"
        threading.Thread(target=cls.server.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()

    def test_headers(self):
        expect = {"/plain": True, "/deny": False, "/sameorigin": False, "/csp-self": False,
                  "/csp-any": True, "/missing": True}
        for path, ok in expect.items():
            with self.subTest(path=path):
                self.assertIs(framable(self.base + path), ok)

    def test_unreachable(self):
        self.assertIsNone(framable("http://127.0.0.1:1/", timeout=1))


class RecordingBrowser(DummyBrowser):
    def __init__(self):
        super().__init__()
        self.opened = []
        self.reloads = 0

    def open(self, url):
        self.opened.append(url)
        super().open(url)

    def _reload(self):
        self.reloads += 1


STAGE_URL = "http://127.0.0.1:8080/stage"


class TestStageBrowser(unittest.TestCase):
    def setUp(self):
        self.inner = RecordingBrowser()
        self.stage = Stage()
        self.checks = []
        self.blocked = {"http://blocked.example/"}

        def check(url):
            self.checks.append(url)
            return url not in self.blocked

        self.browser = StageBrowser(self.inner, self.stage, STAGE_URL, check=check)

    def tearDown(self):
        self.browser.stop()

    def test_framable_pages_go_on_stage(self):
        self.browser.open("http://a.example/")
        self.browser.open("http://b.example/")
        self.assertEqual(self.inner.opened, [STAGE_URL])  # browser navigated once
        self.assertEqual(self.stage.active, "http://b.example/")
        self.assertEqual(self.browser.current_url, "http://b.example/")

    def test_blocked_page_is_shown_directly(self):
        self.browser.open("http://a.example/")
        self.browser.preload("http://blocked.example/")
        self.assertIsNone(self.stage.next)  # never preloaded
        self.browser.open("http://blocked.example/")
        self.assertEqual(self.inner.current_url, "http://blocked.example/")
        self.browser.open("http://a.example/")
        self.assertEqual(self.inner.current_url, STAGE_URL)

    def test_preload_and_switch(self):
        self.browser.open("http://a.example/")
        self.browser.preload("http://b.example/")
        self.assertEqual(self.stage.next, "http://b.example/")
        self.browser.open("http://b.example/")
        self.assertEqual(self.stage.active, "http://b.example/")
        self.assertIsNone(self.stage.next)

    def test_reload(self):
        self.browser.open("http://a.example/")
        self.browser.reload()
        self.assertEqual(self.stage.reloads, 1)
        self.assertEqual(self.inner.reloads, 0)
        self.browser.open("http://blocked.example/")
        self.browser.reload()
        self.assertEqual(self.inner.reloads, 1)

    def test_checks_are_cached_and_own_pages_skipped(self):
        for _ in range(3):
            self.browser.open("http://a.example/")
        self.browser.open("http://127.0.0.1:8080/idle")
        self.assertEqual(self.checks, ["http://a.example/"])

    def test_status(self):
        self.browser.open("http://a.example/")
        st = self.browser.status()
        self.assertEqual(st["url"], "http://a.example/")
        self.assertTrue(st["preload"])


class TestStageWait(unittest.TestCase):
    def test_returns_when_changed(self):
        stage = Stage()
        threading.Timer(0.1, stage.show, args=("http://a.example/",)).start()
        start = time.monotonic()
        state = stage.wait(since=0, timeout=5)
        self.assertLess(time.monotonic() - start, 2)
        self.assertEqual(state["active"], "http://a.example/")

    def test_times_out(self):
        stage = Stage()
        state = stage.wait(since=0, timeout=0.2)
        self.assertEqual(state["seq"], 0)


class TestPlayerPreloads(unittest.TestCase):
    def test_next_page_is_preloaded_before_the_switch(self):
        player_mod.HEALTH_INTERVAL = 0.05
        with tempfile.TemporaryDirectory() as d:
            stage = Stage()
            inner = RecordingBrowser()
            browser = StageBrowser(inner, stage, STAGE_URL, check=lambda url: True)
            player = Player(browser, Store(Path(d) / "p.json"), idle_url="http://127.0.0.1:8080/idle",
                            probe=lambda url: True, preload_ahead=0.6)
            player.start()
            try:
                player.set_playlist(replaced(Playlist(), [Item("http://a.example/", 1), Item("http://b.example/", 1)]))
                seen = []
                deadline = time.time() + 3
                while time.time() < deadline:
                    seen.append((stage.active, stage.next))
                    time.sleep(0.02)
            finally:
                player.stop()
        # b was preloaded while a was showing, then shown, then a preloaded.
        self.assertIn(("http://a.example/", "http://b.example/"), seen)
        self.assertIn(("http://b.example/", "http://a.example/"), seen)
        first_b = next(i for i, s in enumerate(seen) if s[0] == "http://b.example/")
        first_pre = seen.index(("http://a.example/", "http://b.example/"))
        self.assertLess(first_pre, first_b)
        self.assertEqual(inner.opened, [STAGE_URL])


class TestStageApi(unittest.TestCase):
    def test_stage_endpoints(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = Config(port=0, listen="127.0.0.1", state_dir=Path(d), browser="dummy")
            stage = Stage()
            stage.show("http://a.example/")
            player = Player(DummyBrowser(), Store(cfg.playlist_path), idle_url="http://127.0.0.1/idle",
                            probe=lambda url: True)
            server = make_server(Agent(cfg, player, stage=stage))
            base = f"http://127.0.0.1:{server.server_address[1]}"
            threading.Thread(target=server.serve_forever, daemon=True).start()
            try:
                with urllib.request.urlopen(base + "/stage", timeout=5) as r:
                    self.assertIn("iframe", r.read().decode())
                with urllib.request.urlopen(base + "/stage/state?since=-1", timeout=5) as r:
                    state = json.loads(r.read())
                self.assertEqual(state["active"], "http://a.example/")
            finally:
                server.shutdown()
                server.server_close()
                player.browser.stop()


if __name__ == "__main__":
    unittest.main()
