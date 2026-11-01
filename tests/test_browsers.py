import time
import unittest

from eventscreen.browsers import DummyBrowser


class FailingBrowser(DummyBrowser):
    """A browser that can't start, like Cog with no display connected."""

    def command(self, url):
        return ["false"]


class TestRestartBackoff(unittest.TestCase):
    def test_failing_browser_backs_off(self):
        browser = FailingBrowser()
        browser.open("http://a.example/")
        deadline = time.monotonic() + 3.5
        while time.monotonic() < deadline:
            browser.ensure_running()
            time.sleep(0.05)
        # Retries after 2s, then waits 4s: one restart, not dozens.
        self.assertEqual(browser.restarts, 1)
        self.assertIn("exited with code 1", browser.last_error)


if __name__ == "__main__":
    unittest.main()
