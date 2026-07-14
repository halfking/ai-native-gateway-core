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
    evidence_status: str
    request_id: str
    attachment_count: str
    upload_attempts: str
    physical_file_count: str
    manifest_status: str
    client_model: str
    canonical_model: str
    outbound_model: str
    provider_received_model: str
    output: str


SCENARIOS = {
    "S17": (
        "./domains/attachments",
        "TestSaveBase64Image_HashShardedPathAndLegacyRead",
    ),
    "S18": ("./domains/attachments", "TestExtractFromAnthropicBody"),
    "S20": ("./domains/streaming", "TestReadRequestBodyTimeoutClosesSlowBody"),
    "S21": ("./domains/attachments", "TestSaveBase64Image_MaxSize"),
    "S19": (
        "./internal/ir",
        "TestParseGemini_(InlineImageData|FileURI|MixedAttachmentMetadata)",
    ),
    "S22": ("./domains/streaming/executors", "TestPrepareRequestBody_"),
    "S23": (
        "./domains/hooks/compression",
        "TestStripThinkingBlocks_PreservesMultimodalBlocks",
    ),
    "S24": ("./domains/streaming/executors", "TestStateObserver_UserCancelSkipped"),
    "S25": ("./modelname", "TestCanonicalizeClientModel_AlwaysLower"),
    "S26": ("./domains/streaming/executors", "TestResolveOutboundModel_"),
    "S27": ("./modelcatalog", "TestUpsertSQL_LowercaseContract"),
    "S28": ("./discovery", "TestMergeManifestModels_CaseInsensitiveDedup"),
}

PARTIAL_SCENARIOS = {
    "S19": "mixed HTTP/provider evidence is not present in the local Go seam",
    "S22": "no executable provider-A/provider-B upload-count test exists",
    "S23": "no executable compression attachment-reference test exists",
    "S24": "no executable SSE cancel/failover state-trace test exists",
    "S25": "model casing test is not yet isolated as an acceptance seam",
    "S26": "vendor raw casing test is not yet isolated as an acceptance seam",
    "S27": "SQL equality audit requires database-backed evidence",
    "S28": "discovery idempotency requires database-backed evidence",
}


def run_scenario(root: Path, scenario: str) -> ScenarioResult:
    package, test_name = SCENARIOS[scenario]
    command = ["go", "test", package, "-run", f"^{test_name}", "-count=1"]
    completed = subprocess.run(
        command, cwd=root, text=True, capture_output=True, check=False
    )
    output = (completed.stdout + completed.stderr).strip()
    evidence_status = "PASS" if completed.returncode == 0 else "FAIL"
    if scenario in PARTIAL_SCENARIOS and completed.returncode == 0:
        evidence_status = "PARTIAL"
    return ScenarioResult(
        scenario=scenario,
        command=command,
        passed=completed.returncode == 0,
        evidence_status=evidence_status,
        request_id="unknown",
        attachment_count="unknown",
        upload_attempts="unknown",
        physical_file_count="unknown",
        manifest_status="unknown",
        client_model="unknown",
        canonical_model="unknown",
        outbound_model="unknown",
        provider_received_model="unknown",
        output=output,
    )


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
            print(
                f"{result.scenario} {result.evidence_status}: "
                f"{' '.join(result.command)}"
            )
            print(
                "  request_id={0} attachment_count={1} upload_attempts={2} "
                "physical_file_count={3} manifest_status={4} client_model={5} "
                "canonical_model={6} outbound_model={7} "
                "provider_received_model={8}".format(
                    result.request_id,
                    result.attachment_count,
                    result.upload_attempts,
                    result.physical_file_count,
                    result.manifest_status,
                    result.client_model,
                    result.canonical_model,
                    result.outbound_model,
                    result.provider_received_model,
                )
            )
            if result.scenario in PARTIAL_SCENARIOS:
                print(f"  limitation={PARTIAL_SCENARIOS[result.scenario]}")
            if not result.passed and result.output:
                print(result.output)
    return 0 if all(result.passed for result in results) else 1


if __name__ == "__main__":
    sys.exit(main())
