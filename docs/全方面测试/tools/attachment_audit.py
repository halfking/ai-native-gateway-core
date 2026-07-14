#!/usr/bin/env python3
"""Run executable local evidence for attachment Phase 2A scenarios.

This intentionally validates deterministic Go seams. Full supplier, database,
and browser coverage remains gated by Phase 2B/2C/3 dependencies.
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from dataclasses import asdict, dataclass
from pathlib import Path


@dataclass
class ScenarioResult:
    scenario: str
    command: list[str]
    passed: bool
    output: str


SCENARIOS = {
    "S17": (
        "./domains/attachments",
        "TestSaveBase64Image_HashShardedPathAndLegacyRead",
    ),
    "S18": ("./domains/attachments", "TestExtractFromAnthropicBody"),
    "S20": ("./domains/streaming", "TestReadRequestBodyTimeoutClosesSlowBody"),
    "S21": ("./domains/attachments", "TestSaveBase64Image_MaxSize"),
}


def run_scenario(root: Path, scenario: str) -> ScenarioResult:
    package, test_name = SCENARIOS[scenario]
    command = ["go", "test", package, "-run", f"^{test_name}$", "-count=1"]
    completed = subprocess.run(
        command, cwd=root, text=True, capture_output=True, check=False
    )
    output = (completed.stdout + completed.stderr).strip()
    return ScenarioResult(scenario, command, completed.returncode == 0, output)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", choices=sorted(SCENARIOS), action="append")
    parser.add_argument(
        "--json", action="store_true", help="emit machine-readable results"
    )
    args = parser.parse_args()

    root = Path(__file__).resolve().parents[3]
    results = [
        run_scenario(root, scenario) for scenario in args.scenario or sorted(SCENARIOS)
    ]
    if args.json:
        print(
            json.dumps(
                [asdict(result) for result in results], ensure_ascii=False, indent=2
            )
        )
    else:
        for result in results:
            status = "PASS" if result.passed else "FAIL"
            print(f"{result.scenario} {status}: {' '.join(result.command)}")
            if not result.passed and result.output:
                print(result.output)
    return 0 if all(result.passed for result in results) else 1


if __name__ == "__main__":
    sys.exit(main())
