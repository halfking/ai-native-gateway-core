#!/usr/bin/env python3
"""Strict result contract shared by scenario runners and report generation."""

from __future__ import annotations

import json
import os
import subprocess
import time
import uuid
from typing import Any

SCHEMA_VERSION = "1.0"
STATUSES = {"PASS", "FAIL", "SKIPPED", "BLOCKED_ENVIRONMENT", "INVALID"}


def new_run_id(prefix: str = "run") -> str:
    return f"{prefix}-{time.strftime('%Y%m%dT%H%M%SZ', time.gmtime())}-{uuid.uuid4().hex[:8]}"


def git_commit() -> str:
    try:
        return subprocess.check_output(
            ["git", "rev-parse", "HEAD"], stderr=subprocess.DEVNULL, text=True
        ).strip()
    except (OSError, subprocess.CalledProcessError):
        return "unknown"


def envelope(
    scenario: str,
    category: str,
    status: str,
    *,
    run_id: str | None = None,
    checks: dict[str, Any] | None = None,
    metrics: dict[str, Any] | None = None,
    evidence: dict[str, Any] | None = None,
    failures: list[str] | None = None,
    parameters: dict[str, Any] | None = None,
    reason: str | None = None,
) -> dict[str, Any]:
    if status not in STATUSES:
        raise ValueError(f"unsupported result status: {status}")
    result: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "run_id": run_id or os.environ.get("TEST_RUN_ID") or new_run_id(),
        "scenario": scenario,
        "category": category,
        "status": status,
        "commit": git_commit(),
        "gateway": os.environ.get("GATEWAY", ""),
        "checks": checks or {},
        "metrics": metrics or {},
        "evidence": evidence or {},
        "failures": failures or [],
        "parameters": parameters or {},
    }
    if reason:
        result["reason"] = reason
    return result


def write_result(path: str, result: dict[str, Any]) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    temp = f"{path}.tmp.{os.getpid()}"
    with open(temp, "w", encoding="utf-8") as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    os.replace(temp, path)


def validate_result(data: Any, expected_scenario: str | None = None) -> list[str]:
    """Return validation errors; an empty list means the result is valid."""
    if not isinstance(data, dict):
        return ["result must be a JSON object"]
    errors: list[str] = []
    for key in ("schema_version", "run_id", "scenario", "category", "status"):
        if not data.get(key):
            errors.append(f"missing {key}")
    if data.get("schema_version") != SCHEMA_VERSION:
        errors.append(f"unsupported schema_version={data.get('schema_version')!r}")
    if data.get("status") not in STATUSES:
        errors.append(f"invalid status={data.get('status')!r}")
    if expected_scenario and data.get("scenario") != expected_scenario:
        errors.append(f"scenario mismatch: expected {expected_scenario}, got {data.get('scenario')}")
    for key in ("checks", "metrics", "evidence", "parameters"):
        if not isinstance(data.get(key), dict):
            errors.append(f"{key} must be an object")
    if not isinstance(data.get("failures"), list):
        errors.append("failures must be an array")
    checks = data.get("checks")
    if isinstance(checks, dict):
        invalid_checks = [key for key, value in checks.items() if not isinstance(value, bool)]
        if invalid_checks:
            errors.append("checks must contain boolean values: " + ", ".join(invalid_checks))
    if data.get("status") == "PASS":
        false_checks = [key for key, value in (checks or {}).items() if value is False]
        if false_checks:
            errors.append("PASS contains false checks: " + ", ".join(false_checks))
        if data.get("failures"):
            errors.append("PASS contains failures")
    return errors


def normalize_legacy_result(
    data: Any,
    scenario: str,
    *,
    category: str = "functional",
    run_id: str | None = None,
) -> dict[str, Any]:
    """Wrap legacy scenario output without treating legacy claims as proof."""
    if validate_result(data, scenario) == []:
        return data
    legacy = data if isinstance(data, dict) else {"raw": data}
    raw_checks = legacy.get("checks") if isinstance(legacy, dict) else {}
    checks: dict[str, bool] = {}
    if isinstance(raw_checks, dict):
        for key, value in raw_checks.items():
            if isinstance(value, bool):
                checks[key] = value
            elif isinstance(value, str) and value.lower() in {"true", "false"}:
                checks[key] = value.lower() == "true"
            else:
                checks[key] = False
    if isinstance(legacy, dict) and isinstance(legacy.get("passed"), bool):
        checks.setdefault("legacy_passed", legacy["passed"])
    if not checks:
        checks["legacy_contract_present"] = False
    failures: list[str] = []
    if any(value is False for value in checks.values()):
        failures.append("legacy output contains failed or non-boolean checks")
    if isinstance(legacy, dict):
        for key in ("reason", "TODO", "todo", "pending"):
            if legacy.get(key):
                failures.append(f"legacy result contains {key}")
    legacy_status = legacy.get("status") if isinstance(legacy, dict) else None
    if legacy_status == "SKIPPED":
        status = "SKIPPED"
        failures = []
    elif legacy_status == "BLOCKED_ENVIRONMENT":
        status = "BLOCKED_ENVIRONMENT"
    else:
        status = "PASS" if checks and all(checks.values()) and not failures else "FAIL"
    return envelope(
        scenario,
        category,
        status,
        run_id=run_id,
        checks=checks,
        metrics=legacy.get("metrics", {}) if isinstance(legacy, dict) else {},
        evidence={"legacy_result": legacy},
        failures=failures,
        parameters={"legacy_format": True},
        reason="legacy result migrated; acceptance evidence is limited"
        if status == "PASS"
        else "; ".join(failures),
    )


def write_legacy_compatible(path: str, scenario: str, category: str = "functional") -> None:
    with open(path, encoding="utf-8") as stream:
        data = json.load(stream)
    write_result(
        path,
        normalize_legacy_result(
            data,
            scenario,
            category=category,
            run_id=os.environ.get("TEST_RUN_ID"),
        ),
    )
