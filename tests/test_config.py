import tempfile
import unittest
from pathlib import Path

from eventscreen import config

REPO = Path(__file__).resolve().parent.parent


class TestConfig(unittest.TestCase):
    def load(self, text):
        with tempfile.NamedTemporaryFile("w", suffix=".toml", delete=False) as f:
            f.write(text)
        try:
            return config.load(Path(f.name))
        finally:
            Path(f.name).unlink()

    def test_missing_file_gives_defaults(self):
        cfg = config.load(Path("/nonexistent/config.toml"))
        self.assertEqual(cfg.port, 8080)
        self.assertEqual(cfg.browser, "cog")
        self.assertTrue(cfg.wifi_fallback)

    def test_example_config_loads(self):
        # The file the package installs as /etc/eventscreen/config.toml.
        cfg = config.load(REPO / "deploy" / "config.example.toml")
        self.assertEqual(cfg.port, 8080)
        self.assertEqual(cfg.state_dir, Path("/var/lib/eventscreen"))

    def test_values(self):
        cfg = self.load('port = 9000\nname = "Pit"\nstate_dir = "/tmp/x"\nbrowser_args = ["--scale=1.5"]\n')
        self.assertEqual((cfg.port, cfg.name), (9000, "Pit"))
        self.assertEqual(cfg.state_dir, Path("/tmp/x"))
        self.assertEqual(cfg.playlist_path, Path("/tmp/x/playlist.json"))
        self.assertEqual(cfg.browser_args, ["--scale=1.5"])

    def test_unknown_key_is_an_error(self):
        # Catches typos instead of silently ignoring them.
        with self.assertRaisesRegex(ValueError, "prot"):
            self.load("prot = 9000\n")


if __name__ == "__main__":
    unittest.main()
