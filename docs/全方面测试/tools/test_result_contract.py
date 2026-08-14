#!/usr/bin/env python3
"""Regression tests for strict scenario result validation."""

from __future__ import annotations

import sys
import tempfile
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent
if str(TOOLS_DIR) not in sys.path:
    sys.path.insert(0, str(TOOLS_DIR))

from result_contract import envelope, validate_result, write_result
import validation_report


class ResultContractTest(unittest.TestCase):
    def test_rejects_missing_required_p99(self) -> None:
        result = envelope(
            "S99",
            "functional",
            "PASS",
            checks={"observed": True},
            metrics={"p99_required": True},
            evidence={},
            failures=[],
            parameters={},
        )
        self.assertIn(
            "metrics.p99_ms is required when p99_required=true",
            validate_result(result, "S99"),
        )

    def test_rejects_invalid_p99(self) -> None:
        result = envelope(
            "S99",
            "functional",
            "PASS",
            checks={"observed": True},
            metrics={"p99_ms": -1},
            evidence={},
            failures=[],
            parameters={},
        )
        self.assertIn(
            "metrics.p99_ms must be finite and non-negative",
            validate_result(result, "S99"),
        )

    def test_allow_skipped_does_not_allow_blocked_environment(self) -> None:
        with tempfile.TemporaryDirectory() as raw_dir:
            results_dir = Path(raw_dir)
            manifest = results_dir / "manifest.json"
            manifest.write_text('{"scenarios": ["TEST_PREFLIGHT"]}', encoding="utf-8")
            write_result(
                str(results_dir / "TEST_PREFLIGHT.json"),
                envelope(
                    "TEST_PREFLIGHT",
                    "environment",
                    "BLOCKED_ENVIRONMENT",
                    checks={"preflight_passed": False},
                    metrics={"p99_required": False},
                    evidence={},
                    failures=["database unavailable"],
                    parameters={},
                ),
            )
            self.assertEqual(
                1,
                validation_report.main(
                    ["--results", str(results_dir), "--manifest", str(manifest), "--allow-skipped"]
                ),
            )


if __name__ == "__main__":
    unittest.main()
