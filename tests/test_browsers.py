import subprocess
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


class SlowBrowser(DummyBrowser):
    """Alive, but too busy to answer a navigate in time."""

    def _navigate(self, url):
        raise subprocess.TimeoutExpired(["cogctl", "open", url], 15)


class BrokenBrowser(DummyBrowser):
    def _navigate(self, url):
        raise subprocess.CalledProcessError(1, ["cogctl", "open", url])


class TestNavigateFailures(unittest.TestCase):
    def test_slow_answer_does_not_restart(self):
        browser = SlowBrowser()
        try:
            browser.open("http://a.example/")
            pid = browser.proc.pid
            browser.open("http://b.example/")
            self.assertEqual(browser.proc.pid, pid)
            self.assertEqual(browser.restarts, 0)
            self.assertEqual(browser.current_url, "http://b.example/")
        finally:
            browser.stop()

    def test_real_failure_restarts(self):
        browser = BrokenBrowser()
        try:
            browser.open("http://a.example/")
            browser.open("http://b.example/")
            self.assertEqual(browser.restarts, 1)
            self.assertEqual(browser.current_url, "http://b.example/")
        finally:
            browser.stop()


if __name__ == "__main__":
    unittest.main()
