"""Builds the .deb with an explicit version, the way a release tag does."""

import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent


@unittest.skipUnless(shutil.which("dpkg-deb") and shutil.which("bash"), "needs dpkg-deb")
class TestBuildDeb(unittest.TestCase):
    def test_version_is_stamped_into_the_package(self):
        version = "9.8.7~rc1"
        with tempfile.TemporaryDirectory() as d:
            src = Path(d) / "src"
            # Build from a copy so the test never touches the repo's dist/.
            shutil.copytree(REPO, src, ignore=shutil.ignore_patterns(".git", "dist", "__pycache__"))
            r = subprocess.run(["bash", str(src / "packaging/build-deb.sh"), version],
                               capture_output=True, text=True)
            self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
            deb = src / "dist" / f"eventscreen_{version}_all.deb"
            self.assertTrue(deb.exists())
            field = subprocess.run(["dpkg-deb", "-f", str(deb), "Version"], capture_output=True, text=True)
            self.assertEqual(field.stdout.strip(), version)
            out = Path(d) / "x"
            subprocess.run(["dpkg-deb", "-x", str(deb), str(out)], check=True)
            init = (out / "usr/lib/eventscreen/eventscreen/__init__.py").read_text()
            self.assertIn(f'__version__ = "{version}"', init)
            # The repo itself is left alone.
            self.assertNotIn(version, (REPO / "eventscreen/__init__.py").read_text())


if __name__ == "__main__":
    unittest.main()
