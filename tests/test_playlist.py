import tempfile
import unittest
from pathlib import Path

from eventscreen.playlist import Item, Playlist, PlaylistError, Store, parse_items, replaced


class TestParseItems(unittest.TestCase):
    def test_strings_and_objects(self):
        items = parse_items(["http://a.example/",
                             {"url": "https://b.example/x", "duration": 12, "label": "B", "id": "b"}],
                            default_duration=30, min_duration=5)
        self.assertEqual(items[0], Item("http://a.example/", 30))
        self.assertEqual(items[1], Item("https://b.example/x", 12, "B", "b"))

    def test_rejects_bad_input(self):
        bad = [
            "not a list",
            [""],
            ["ftp://a.example/"],
            ["/relative"],
            ["javascript:alert(1)"],
            [{"url": "http://a.example/", "duration": 1}],      # below the minimum
            [{"url": "http://a.example/", "duration": "30"}],   # not a number
            [{"url": "http://a.example/", "duration": True}],
            [42],
        ]
        for data in bad:
            with self.subTest(data=data), self.assertRaises(PlaylistError):
                parse_items(data, default_duration=30, min_duration=5)

    def test_error_names_the_item(self):
        with self.assertRaisesRegex(PlaylistError, r"items\[1\]"):
            parse_items(["http://a.example/", "nope"], 30, 5)


class TestStore(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.path = Path(self.tmp.name) / "sub" / "playlist.json"
        self.store = Store(self.path)

    def tearDown(self):
        self.tmp.cleanup()

    def test_missing_file_is_empty(self):
        self.assertEqual(self.store.load().items, [])

    def test_round_trip(self):
        pl = replaced(Playlist(), [Item("http://a.example/", 10, "A")], source="event-7")
        self.store.save(pl)
        loaded = self.store.load()
        self.assertEqual(loaded.items, pl.items)
        self.assertEqual(loaded.revision, 1)
        self.assertEqual(loaded.source, "event-7")
        self.assertFalse(self.path.with_suffix(".tmp").exists())

    def test_damaged_file_starts_empty(self):
        self.path.parent.mkdir(parents=True)
        self.path.write_text('{"items": [{"url": "http://a.exa')  # cut off by a power cut
        with self.assertLogs("eventscreen.playlist", "ERROR"):
            self.assertEqual(self.store.load().items, [])
        self.assertTrue(self.path.with_suffix(".bad").exists())


if __name__ == "__main__":
    unittest.main()
