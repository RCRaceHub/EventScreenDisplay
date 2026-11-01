import tempfile
import threading
import unittest
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from eventscreen import wifi
from eventscreen.config import Config


class Clock:
    def __init__(self):
        self.t = 1000.0

    def __call__(self):
        return self.t


class FakeScreen:
    def __init__(self):
        self.shown = False

    def show(self):
        self.shown = True

    def clear(self):
        self.shown = False


class FakeNM:
    """Stands in for NetworkManager in the Fallback state machine."""

    def __init__(self):
        self.is_online = True
        self.hotspot = False
        self.join_ok = True
        self.saved = []
        self.hotspot_starts = 0

    def online(self):
        return self.is_online

    def hotspot_active(self):
        return self.hotspot

    def scan(self):
        return [wifi.Network("Venue", 80, True), wifi.Network("EventScreen-test", 99, True)]

    def start_hotspot(self, ssid, password):
        self.hotspot = True
        self.hotspot_starts += 1

    def stop_hotspot(self):
        self.hotspot = False

    def save_network(self, ssid, password, hidden=False):
        self.saved.append((ssid, password, hidden))
        return f"wifi-{ssid}"

    def connect(self, name, wait=0):
        self.is_online = self.join_ok
        return self.join_ok


def make_fallback(**cfg):
    nm, screen, clock = FakeNM(), FakeScreen(), Clock()
    fb = wifi.Fallback(nm, Config(**cfg), screen, "EventScreen-test", "secret123", clock=clock)
    return fb, nm, screen, clock


class TestSplitTerse(unittest.TestCase):
    def test_escapes(self):
        self.assertEqual(wifi.split_terse(r"My\:Net:72:WPA2"), ["My:Net", "72", "WPA2"])
        self.assertEqual(wifi.split_terse(r"a\\b::c"), ["a\\b", "", "c"])


class TestNetworkManager(unittest.TestCase):
    def test_parsing(self):
        outputs = {
            "device": "wlan0:wifi:connected:eventscreen-hotspot\neth0:ethernet:unavailable:\nlo:loopback:unmanaged:\n",
            "list": "Venue:40:WPA2\nVenue:75:WPA2\nCafe\\:Free:30:\n:90:WPA2\n",
        }
        calls = []

        def run(args, timeout):
            calls.append(args)
            return outputs["list" if "list" in args else "device"]

        nm = wifi.NetworkManager(run)
        self.assertTrue(nm.hotspot_active())
        self.assertFalse(nm.online())
        self.assertTrue(nm.has_wifi())
        nets = nm.scan()
        self.assertEqual([(n.ssid, n.signal, n.secure) for n in nets],
                         [("Venue", 75, True), ("Cafe:Free", 30, False)])

    def test_online_on_wifi(self):
        nm = wifi.NetworkManager(lambda a, t: "wlan0:wifi:connected:wifi-Venue\n")
        self.assertTrue(nm.online())
        self.assertFalse(nm.hotspot_active())

    def test_save_network(self):
        calls = []

        def run(args, timeout):
            calls.append(args)
            return ""

        wifi.NetworkManager(run).save_network("Venue", "password1", hidden=True)
        add = calls[-1]
        self.assertEqual(add[:2], ["connection", "add"])
        self.assertIn("wifi-Venue", add)
        self.assertIn("password1", add)
        self.assertIn("802-11-wireless.hidden", add)


class TestFallback(unittest.TestCase):
    def test_stays_quiet_while_online(self):
        fb, nm, screen, clock = make_fallback()
        for _ in range(50):
            clock.t += 10
            fb.tick()
        self.assertEqual(fb.state, "client")
        self.assertFalse(nm.hotspot)

    def test_hotspot_after_timeout(self):
        fb, nm, screen, clock = make_fallback(wifi_fallback_after=120)
        nm.is_online = False
        clock.t += 110
        fb.tick()
        self.assertFalse(nm.hotspot)
        clock.t += 10
        fb.tick()
        self.assertEqual(fb.state, "hotspot")
        self.assertTrue(nm.hotspot)
        self.assertTrue(screen.shown)
        # Its own hotspot isn't offered as a network to join.
        self.assertEqual([n.ssid for n in fb.networks], ["Venue"])

    def test_retry_known_networks(self):
        fb, nm, screen, clock = make_fallback(wifi_fallback_after=120, wifi_retry_interval=300)
        nm.is_online = False
        clock.t += 120
        fb.tick()
        clock.t += 300
        fb.tick()
        self.assertEqual(fb.state, "client")
        self.assertFalse(nm.hotspot)
        self.assertFalse(screen.shown)
        # Back up after the retry window if still offline.
        clock.t += wifi.RETRY_WINDOW - 1
        fb.tick()
        self.assertFalse(nm.hotspot)
        clock.t += 1
        fb.tick()
        self.assertTrue(nm.hotspot)

    def test_no_retry_while_someone_is_setting_up(self):
        fb, nm, screen, clock = make_fallback(wifi_fallback_after=120, wifi_retry_interval=300)
        nm.is_online = False
        clock.t += 120
        fb.tick()
        clock.t += 290
        fb.activity()
        clock.t += 10
        fb.tick()
        self.assertEqual(fb.state, "hotspot")

    def test_restarts_dead_hotspot(self):
        fb, nm, screen, clock = make_fallback()
        nm.is_online = False
        clock.t += 120
        fb.tick()
        nm.hotspot = False
        clock.t += 10
        fb.tick()
        self.assertEqual(nm.hotspot_starts, 2)

    def test_ethernet_ends_setup(self):
        fb, nm, screen, clock = make_fallback()
        nm.is_online = False
        clock.t += 120
        fb.tick()
        nm.is_online = True
        clock.t += 10
        fb.tick()
        self.assertEqual(fb.state, "client")
        self.assertFalse(nm.hotspot)

    def test_choose_success(self):
        fb, nm, screen, clock = make_fallback()
        nm.is_online = False
        clock.t += 120
        fb.tick()
        self.assertTrue(fb.choose("Venue", "password1"))
        self.assertEqual(nm.saved, [("Venue", "password1", False)])
        self.assertEqual(fb.state, "client")
        self.assertFalse(nm.hotspot)
        self.assertFalse(screen.shown)
        fb.tick()
        self.assertEqual(fb.state, "client")

    def test_choose_failure_brings_hotspot_back(self):
        fb, nm, screen, clock = make_fallback()
        nm.is_online = False
        nm.join_ok = False
        clock.t += 120
        fb.tick()
        self.assertFalse(fb.choose("Venue", "wrongpass"))
        self.assertEqual(fb.state, "hotspot")
        self.assertTrue(nm.hotspot)
        self.assertIn("Couldn't join Venue", fb.message)


class TestValidate(unittest.TestCase):
    def test_validate(self):
        self.assertEqual(wifi.validate("Venue", "password1"), "")
        self.assertEqual(wifi.validate("Open", ""), "")
        self.assertTrue(wifi.validate("", "password1"))
        self.assertTrue(wifi.validate("x" * 33, ""))
        self.assertTrue(wifi.validate("Venue", "short"))


class TestCredentials(unittest.TestCase):
    def test_generated_password_is_kept(self):
        with tempfile.TemporaryDirectory() as d:
            cfg = Config(state_dir=Path(d))
            ssid, pw = wifi.hotspot_credentials(cfg)
            self.assertTrue(ssid.startswith("EventScreen-"))
            self.assertGreaterEqual(len(pw), 8)
            self.assertEqual(wifi.hotspot_credentials(cfg)[1], pw)
            self.assertEqual((Path(d) / "hotspot-password").stat().st_mode & 0o777, 0o600)

    def test_configured(self):
        cfg = Config(hotspot_ssid="Pit", hotspot_password="letmein99")
        self.assertEqual(wifi.hotspot_credentials(cfg), ("Pit", "letmein99"))
        with self.assertRaises(ValueError):
            wifi.hotspot_credentials(Config(hotspot_password="short"))


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *args, **kwargs):
        return None


class TestSetupPage(unittest.TestCase):
    def setUp(self):
        self.fb, self.nm, _, clock = make_fallback()
        self.nm.is_online = False
        clock.t += 120
        self.fb.tick()
        self.server = wifi.SetupServer(self.fb, addr="127.0.0.1", port=0)
        self.server.start()
        self.base = f"http://127.0.0.1:{self.server.httpd.server_address[1]}"
        self.opener = urllib.request.build_opener(NoRedirect)

    def tearDown(self):
        self.server.stop()

    def get(self, path):
        try:
            with self.opener.open(self.base + path, timeout=5) as r:
                return r.status, r.read().decode(), r.headers
        except urllib.error.HTTPError as e:
            return e.code, e.read().decode(), e.headers

    def post(self, form):
        data = urllib.parse.urlencode(form).encode()
        with urllib.request.urlopen(self.base + "/", data=data, timeout=5) as r:
            return r.read().decode()

    def test_pages(self):
        status, body, _ = self.get("/")
        self.assertEqual(status, 200)
        self.assertIn("Venue", body)
        status, body, _ = self.get("/screen")
        self.assertIn("secret123", body)
        self.assertIn("EventScreen-test", body)
        status, _, headers = self.get("/generate_204")
        self.assertEqual(status, 302)
        self.assertEqual(headers["Location"], wifi.SETUP_URL)

    def test_post_invalid(self):
        body = self.post({"ssid": "Venue", "password": "short"})
        self.assertIn("8 to 63", body)
        self.assertEqual(self.nm.saved, [])

    def test_post_joins(self):
        done = threading.Event()
        original = self.fb.choose

        def choose(*args):
            try:
                return original(*args)
            finally:
                done.set()

        self.fb.choose = choose
        body = self.post({"ssid": "Venue", "other": "Typed Net", "password": "password1"})
        self.assertIn("Connecting to Typed Net", body)
        self.assertTrue(done.wait(5))
        self.assertEqual(self.nm.saved, [("Typed Net", "password1", False)])


if __name__ == "__main__":
    unittest.main()
