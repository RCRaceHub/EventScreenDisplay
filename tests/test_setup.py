"""Runs deploy/eventscreen-setup against a scratch root with fake system tools."""

import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
SETUP = REPO / "deploy" / "eventscreen-setup"

FAKES = {
    # Record every call so tests can see what would have happened.
    "systemctl": 'echo "systemctl $*" >> "$EVENTSCREEN_ROOT/calls"\n'
                 '[ "$1" = is-enabled ] && [ -f "$EVENTSCREEN_ROOT/getty-enabled" ] && exit 0\n'
                 '[ "$1" = is-enabled ] && exit 1\nexit 0',
    "hostnamectl": 'echo "hostnamectl $*" >> "$EVENTSCREEN_ROOT/calls"',
    "hostname": "echo raspberrypi",
    "apt-get": 'echo "apt-get $*" >> "$EVENTSCREEN_ROOT/calls"',
}


@unittest.skipUnless(shutil.which("bash"), "needs bash")
class TestSetup(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        (self.root / "etc/eventscreen").mkdir(parents=True)
        shutil.copy(REPO / "deploy/config.example.toml", self.root / "etc/eventscreen/config.toml")
        (self.root / "etc/hosts").write_text("127.0.0.1\tlocalhost\n127.0.1.1\traspberrypi\n")
        (self.root / "boot/firmware").mkdir(parents=True)
        self.cmdline = self.root / "boot/firmware/cmdline.txt"
        self.cmdline.write_text("console=tty1 root=PARTUUID=1234-02 rootwait\n")
        (self.root / "getty-enabled").touch()
        bindir = self.root / "bin"
        bindir.mkdir()
        for name, body in FAKES.items():
            f = bindir / name
            f.write_text("#!/bin/sh\n" + body + "\n")
            f.chmod(0o755)
        self.env = {**os.environ, "EVENTSCREEN_ROOT": str(self.root),
                    "PATH": f"{bindir}:{os.environ['PATH']}"}

    def tearDown(self):
        self.tmp.cleanup()

    def run_setup(self, *args):
        r = subprocess.run(["bash", str(SETUP), *args], env=self.env, capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        return r.stdout

    def conf(self):
        return (self.root / "etc/eventscreen/config.toml").read_text()

    def calls(self):
        return (self.root / "calls").read_text()

    def test_defaults(self):
        out = self.run_setup()
        cmdline = self.cmdline.read_text()
        self.assertIn("video=HDMI-A-1:1920x1080@60D", cmdline)
        self.assertIn("consoleblank=0", cmdline)
        self.assertEqual(len(cmdline.splitlines()), 1, "cmdline.txt must stay one line")
        self.assertIn("systemctl disable getty@tty1.service", self.calls())
        self.assertIn("systemctl restart eventscreen", self.calls())
        self.assertIn("Reboot", out)

    def test_idempotent(self):
        self.run_setup()
        first = self.cmdline.read_text()
        (self.root / "getty-enabled").unlink()
        out = self.run_setup()
        self.assertEqual(self.cmdline.read_text(), first)
        self.assertNotIn("Reboot", out)

    def test_options_write_config(self):
        self.run_setup("--hostname", "screen-07", "--name", "Pit lane", "--port", "9090",
                       "--token", "s3cret", "--browser", "dummy")
        conf = self.conf()
        self.assertIn('name = "Pit lane"', conf)
        self.assertIn("port = 9090", conf)
        self.assertIn('api_token = "s3cret"', conf)
        self.assertIn('browser = "dummy"', conf)
        self.assertIn("hostnamectl set-hostname screen-07", self.calls())
        self.assertIn("127.0.1.1\tscreen-07", (self.root / "etc/hosts").read_text())
        # The result must still be valid config.
        from eventscreen import config
        cfg = config.load(self.root / "etc/eventscreen/config.toml")
        self.assertEqual((cfg.port, cfg.api_token), (9090, "s3cret"))

    def test_resolution(self):
        self.run_setup("--resolution", "1920x1080@50")
        self.assertIn("video=HDMI-A-1:1920x1080@50", self.cmdline.read_text())
        self.run_setup("--resolution", "auto")
        self.assertNotIn("video=", self.cmdline.read_text())

    def test_upgrades_old_default(self):
        # 0.2.0 wrote @60 without the D that forces HDMI on.
        self.cmdline.write_text("console=tty1 rootwait video=HDMI-A-1:1920x1080@60 consoleblank=0\n")
        self.run_setup()
        self.assertIn("video=HDMI-A-1:1920x1080@60D consoleblank=0", self.cmdline.read_text())

    def test_keeps_custom_mode(self):
        self.cmdline.write_text("console=tty1 video=HDMI-A-1:1920x1080@60D,rotate=90\n")
        self.run_setup()
        self.assertIn("video=HDMI-A-1:1920x1080@60D,rotate=90", self.cmdline.read_text())


if __name__ == "__main__":
    unittest.main()
