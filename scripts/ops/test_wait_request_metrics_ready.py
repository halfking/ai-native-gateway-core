import runpy
from datetime import datetime, timezone
from pathlib import Path
import unittest


timestamp = runpy.run_path(str(Path(__file__).with_name("wait-request-metrics-ready.py")))["timestamp"]


class ProjectionTimestampTest(unittest.TestCase):
    def test_postgresql_precision(self):
        for suffix, microseconds in (
            ("", 0),
            (".5", 500000),
            (".58", 580000),
            (".584", 584000),
            (".5843", 584300),
            (".58439", 584390),
            (".584391", 584391),
        ):
            with self.subTest(suffix=suffix):
                parsed = timestamp("2026-11-24T13:57:33" + suffix + "Z")
                self.assertEqual(parsed, datetime(2026, 11, 24, 13, 57, 33, microseconds, timezone.utc))

    def test_timezone(self):
        self.assertEqual(
            timestamp("2026-11-24T21:57:33.58439+08:00"),
            timestamp("2026-11-24T13:57:33.58439Z"),
        )
        self.assertIsNone(timestamp(None))
        with self.assertRaises(ValueError):
            timestamp("2026-11-24T13:57:33.58439")


if __name__ == "__main__":
    unittest.main()
