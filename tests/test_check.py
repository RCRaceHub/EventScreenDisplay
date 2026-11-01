import fnmatch
import io
import unittest
from contextlib import redirect_stdout
from unittest import mock

from eventscreen import check
from eventscreen.config import Config

HEALTHY = {
    "state": "playing",
    "browser": {"running": True, "pid": 123, "restarts": 0, "last_error": ""},
    "current": {"url": "http://timing.local/board"},
    "network": {"page_reachable": True},
}


class FakeProbe(check.Probe):
    """A healthy Pi; tests break one thing at a time."""

    def __init__(self, cfg=None):
        super().__init__(cfg or Config())
        self.services = {"eventscreen": "active", "avahi-daemon": "active", "eventscreen-wifi": "active"}
        self.files = {
            "/sys/class/drm/card1-HDMI-A-1/status": "connected\n",
            "/boot/firmware/cmdline.txt": "console=tty1 rootwait video=HDMI-A-1:1920x1080@60D consoleblank=0\n",
            "/etc/avahi/services/eventscreen.service": "<service-group/>",
            "/etc/hostname": "screen-01\n",
            "/usr/lib/aarch64-linux-gnu/libGLESv2.so.2": "",
        }
        self.binaries = {"cog", "cogctl", "nmcli"}
        self.nmcli = "wifi:connected:wifi-Venue\nloopback:connected (externally):lo\n"
        self.throttled = "throttled=0x0"
        self.status = dict(HEALTHY)

    def run(self, *cmd):
        if cmd[0] == "systemctl":
            return 0, self.services.get(cmd[2], "inactive")
        if cmd[0] == "nmcli":
            return 0, self.nmcli
        if cmd[0] == "vcgencmd":
            return 0, self.throttled
        return 127, "not found"

    def which(self, binary):
        return f"/usr/bin/{binary}" if binary in self.binaries else None

    def read(self, path):
        return self.files.get(path)

    def glob(self, pattern):
        return sorted(p for p in self.files if fnmatch.fnmatch(p, pattern))

    def api_status(self):
        if self.status is None:
            raise ConnectionRefusedError("refused")
        return self.status


def levels(probe):
    return {r.name: r.level for r in check.run_checks(probe)}


class TestCheck(unittest.TestCase):
    def test_healthy_pi_passes(self):
        results = levels(FakeProbe())
        self.assertEqual(set(results.values()), {check.PASS}, results)

    def test_hdmi_disconnected(self):
        p = FakeProbe()
        p.files["/sys/class/drm/card1-HDMI-A-1/status"] = "disconnected\n"
        self.assertEqual(levels(p)["HDMI"], check.FAIL)

    def test_display_mode_not_forced(self):
        p = FakeProbe()
        p.files["/boot/firmware/cmdline.txt"] = "console=tty1 video=HDMI-A-1:1920x1080@60\n"
        self.assertEqual(levels(p)["display mode"], check.WARN)

    def test_agent_down(self):
        p = FakeProbe()
        p.services["eventscreen"] = "failed"
        p.status = None
        r = levels(p)
        self.assertEqual(r["service eventscreen"], check.FAIL)
        self.assertEqual(r["API"], check.FAIL)

    def test_browser_not_running(self):
        p = FakeProbe()
        p.status = {**HEALTHY, "browser": {"running": False, "last_error": "browser exited with code 1"}}
        self.assertEqual(levels(p)["browser running"], check.FAIL)

    def test_cog_missing(self):
        p = FakeProbe()
        p.binaries.discard("cog")
        self.assertEqual(levels(p)["browser"], check.FAIL)

    def test_cog_without_gles(self):
        # Pi OS Lite installs cog without it, and every page then crashes.
        p = FakeProbe()
        del p.files["/usr/lib/aarch64-linux-gnu/libGLESv2.so.2"]
        results = {r.name: r for r in check.run_checks(p)}
        self.assertEqual(results["browser"].level, check.FAIL)
        self.assertIn("libgles2", results["browser"].detail)

    def test_page_server_unreachable(self):
        p = FakeProbe()
        p.status = {**HEALTHY, "network": {"page_reachable": False}}
        self.assertEqual(levels(p)["page server"], check.FAIL)

    def test_setup_hotspot(self):
        p = FakeProbe()
        p.nmcli = "wifi:connected:eventscreen-hotspot\n"
        self.assertEqual(levels(p)["network"], check.FAIL)

    def test_ethernet_counts(self):
        p = FakeProbe()
        p.nmcli = "ethernet:connected:Wired connection 1\nwifi:disconnected:\n"
        self.assertEqual(levels(p)["network"], check.PASS)

    def test_under_voltage(self):
        p = FakeProbe()
        p.throttled = "throttled=0x50005"
        self.assertEqual(levels(p)["power"], check.WARN)

    def test_wifi_service_optional_when_fallback_off(self):
        p = FakeProbe(Config(wifi_fallback=False))
        p.services["eventscreen-wifi"] = "failed"
        self.assertEqual(levels(p)["service eventscreen-wifi"], check.WARN)

    def test_main_exit_code(self):
        with mock.patch.object(check, "Probe", FakeProbe), redirect_stdout(io.StringIO()) as out:
            self.assertEqual(check.main(["--config", "/nonexistent.toml"]), 0)
        self.assertIn("All good.", out.getvalue())
        broken = type("Broken", (FakeProbe,), {"api_status": lambda self: (_ for _ in ()).throw(OSError("x"))})
        with mock.patch.object(check, "Probe", broken), redirect_stdout(io.StringIO()):
            self.assertEqual(check.main(["--config", "/nonexistent.toml", "--json"]), 1)


if __name__ == "__main__":
    unittest.main()
