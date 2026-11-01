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
from eventscreen.messages import MessageError, Messages, parse_banner, parse_message
from eventscreen.player import Player
from eventscreen.playlist import Item, Playlist, Store, replaced
from eventscreen.stage import Stage, StageBrowser


class Clock:
    def __init__(self):
        self.t = 1000.0

    def __call__(self):
        return self.t


class TestParsing(unittest.TestCase):
    def test_defaults(self):
        m = parse_message({"title": "Finals being calculated"}, now=10)
        self.assertEqual((m["style"], m["level"], m["text"], m["until"]), ("hold", "info", "", None))
        m = parse_message({"title": "Finals are up", "style": "announce", "duration": 60}, now=10)
        self.assertEqual(m["until"], 70)

    def test_rejects(self):
        bad = [{}, {"title": ""}, {"title": "x" * 121}, {"title": "a", "style": "flash"},
               {"title": "a", "level": "red"}, {"title": "a", "duration": 0},
               {"title": "a", "duration": "60"}, {"title": 5}]
        for body in bad:
            with self.subTest(body=body), self.assertRaises(MessageError):
                parse_message(body, now=0)
        with self.assertRaises(MessageError):
            parse_banner({"text": " "}, now=0)


class TestMessages(unittest.TestCase):
    def test_expiry_and_persistence(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "messages.json"
            clock = Clock()
            msgs = Messages(path, clock=clock)
            calls = []
            msgs.on_change(lambda: calls.append(1))
            msgs.set_message({"title": "Hold", "duration": 30})
            msgs.set_banner({"text": "Car 12 to the pits"})
            self.assertEqual(len(calls), 2)
            # Survives a restart.
            again = Messages(path, clock=clock)
            self.assertEqual(again.message["title"], "Hold")
            self.assertEqual(again.banner["text"], "Car 12 to the pits")
            clock.t += 30
            self.assertIsNone(msgs.message)
            self.assertIsNotNone(msgs.banner)
            # Expired messages aren't brought back after a restart.
            self.assertIsNone(Messages(path, clock=clock).message)
            msgs.clear_banner()
            self.assertIsNone(Messages(path, clock=clock).banner)

    def test_damaged_file_is_ignored(self):
        with tempfile.TemporaryDirectory() as d:
            path = Path(d) / "messages.json"
            path.write_text("{not json")
            with self.assertLogs("eventscreen.messages", "ERROR"):
                self.assertIsNone(Messages(path).message)


class PlayerTest(unittest.TestCase):
    def setUp(self):
        player_mod.HEALTH_INTERVAL = 0.05
        self.tmp = tempfile.TemporaryDirectory()
        self.messages = Messages(Path(self.tmp.name) / "m.json")
        self.browser = DummyBrowser()
        self.player = Player(self.browser, Store(Path(self.tmp.name) / "p.json"),
                             idle_url="http://127.0.0.1/idle", probe=lambda u: True,
                             messages=self.messages, message_url="http://127.0.0.1/message")
        self.player.start()

    def tearDown(self):
        self.player.stop()
        self.tmp.cleanup()

    def wait_url(self, url, timeout=3):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.browser.current_url == url:
                return
            time.sleep(0.02)
        self.fail(f"browser on {self.browser.current_url}, wanted {url}")


class TestPlayerMessages(PlayerTest):
    def test_finals_flow(self):
        # Qualifying playlist running...
        self.player.set_playlist(replaced(Playlist(), [Item("http://rctc/quali", 30)]))
        self.wait_url("http://rctc/quali")
        # ...hold while finals are calculated...
        self.messages.set_message({"title": "Finals being calculated"})
        self.wait_url("http://127.0.0.1/message")
        self.assertEqual(self.player.status()["state"], "message")
        # ...announce for a moment, with the finals playlist pushed underneath...
        self.messages.set_message({"title": "Finals lists are up", "style": "announce", "duration": 1})
        self.player.set_playlist(replaced(self.player.playlist, [Item("http://rctc/finals", 30)]))
        time.sleep(0.3)
        self.assertEqual(self.browser.current_url, "http://127.0.0.1/message")
        # ...then the finals boards, by themselves.
        self.wait_url("http://rctc/finals")
        self.assertEqual(self.player.status()["state"], "playing")

    def test_message_covers_show_and_wifi_setup_covers_message(self):
        self.player.show(Item("http://podium", 0), None)
        self.wait_url("http://podium")
        self.messages.set_message({"title": "Hold"})
        self.wait_url("http://127.0.0.1/message")
        self.player.show(Item("http://10.42.0.1/screen", 0), None, above_messages=True)
        self.wait_url("http://10.42.0.1/screen")
        self.player.clear_override()
        self.wait_url("http://127.0.0.1/message")
        self.messages.clear_message()
        self.wait_url("http://127.0.0.1/idle")


class TestApi(unittest.TestCase):
    token = "s3cret"

    def setUp(self):
        player_mod.HEALTH_INTERVAL = 0.05
        self.tmp = tempfile.TemporaryDirectory()
        tmp = Path(self.tmp.name)
        self.cfg = Config(port=0, listen="127.0.0.1", state_dir=tmp, browser="dummy", api_token=self.token)
        self.messages = Messages(tmp / "m.json")
        self.stage = Stage(self.messages)
        self.inner = DummyBrowser()
        browser = StageBrowser(self.inner, self.stage, "http://127.0.0.1:1/stage", check=lambda u: True)
        self.player = Player(browser, Store(tmp / "p.json"), idle_url="http://127.0.0.1:1/idle",
                             probe=lambda u: True, messages=self.messages,
                             message_url="http://127.0.0.1:1/message")
        self.server = make_server(Agent(self.cfg, self.player, stage=self.stage, messages=self.messages))
        self.base = f"http://127.0.0.1:{self.server.server_address[1]}"
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.player.start()

    def tearDown(self):
        self.server.shutdown()
        self.server.server_close()
        self.player.stop()
        self.tmp.cleanup()

    def call(self, method, path, body=None, token=None):
        token = self.token if token is None else token
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", f"Bearer {token}")
        try:
            with urllib.request.urlopen(req, timeout=5) as r:
                return r.status, r.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode()

    def test_message_endpoints(self):
        status, body = self.call("PUT", "/api/v1/message",
                                 {"title": "Finals being calculated", "text": "Please wait"})
        self.assertEqual(status, 200)
        self.assertEqual(json.loads(body)["message"]["style"], "hold")
        _, body = self.call("GET", "/api/v1/status")
        st = json.loads(body)
        self.assertEqual(st["message"]["title"], "Finals being calculated")
        status, _ = self.call("DELETE", "/api/v1/message")
        self.assertEqual(status, 200)
        _, body = self.call("GET", "/api/v1/message")
        self.assertIsNone(json.loads(body)["message"])
        status, body = self.call("PUT", "/api/v1/message", {"title": "x", "level": "red"})
        self.assertEqual(status, 400)
        self.assertIn("level", body)

    def test_banner_endpoints_and_stage_state(self):
        self.player.set_playlist(replaced(Playlist(), [Item("http://rctc/board", 30)]))
        time.sleep(0.2)
        status, body = self.call("POST", "/api/v1/banner",
                                 {"text": "Car 12 to the pits", "level": "warning", "duration": 30})
        self.assertEqual(status, 200)
        banner = json.loads(body)["banner"]
        self.assertTrue(banner["visible"])
        self.assertEqual(self.stage.state()["banner"]["text"], "Car 12 to the pits")
        self.call("DELETE", "/api/v1/banner")
        self.assertIsNone(self.stage.state()["banner"])

    def test_writes_need_token(self):
        status, _ = self.call("PUT", "/api/v1/message", {"title": "x"}, token="wrong")
        self.assertEqual(status, 401)

    def test_message_page(self):
        status, body = self.call("GET", "/message")
        self.assertEqual(status, 200)
        self.assertIn("/api/v1/message", body)
        _, body = self.call("GET", "/api/v1/info")
        self.assertIn("banner", json.loads(body)["features"])


if __name__ == "__main__":
    unittest.main()
