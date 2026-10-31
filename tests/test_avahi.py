import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

from eventscreen import avahi


class TestAvahi(unittest.TestCase):
    def test_service_file_follows_config(self):
        with tempfile.TemporaryDirectory() as tmp:
            conf = Path(tmp, "config.toml")
            conf.write_text('screen_id = "pit-lane"\nport = 9000\n')
            out = Path(tmp, "eventscreen.service")
            avahi.main(["--config", str(conf), "--output", str(out)])
            root = ET.fromstring(out.read_text().split("\n", 2)[2])
            services = {s.findtext("type"): s for s in root.iter("service")}
            es = services["_eventscreen._tcp"]
            self.assertEqual(es.findtext("port"), "9000")
            self.assertIn("id=pit-lane", [t.text for t in es.iter("txt-record")])
            self.assertEqual(services["_http._tcp"].findtext("port"), "9000")


if __name__ == "__main__":
    unittest.main()
