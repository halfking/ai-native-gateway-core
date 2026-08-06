#!/usr/bin/env python3
"""Strict, run-scoped acceptance report for the gateway test suites."""

from __future__ import annotations

import argparse
import glob
import json
import os
import sys
from datetime import datetime, timezone
from typing import Any

from result_contract import STATUSES, normalize_legacy_result, validate_result

# Legacy load-test gates retained as a compatibility reference. Strict scenario
# checks are authoritative when a result uses schema_version 1.0.
GATES = {
    "S01_baseline": {"succ_pct": 99, "p99_ms": 1500, "label": "基准性能"},
    "S02_cost_route": {"succ_pct": 99, "p99_ms": 1500, "label": "成本优化路由"},
    "S03_concurrency_diff": {"succ_pct": 99, "p99_ms": 1500, "label": "并发能力差异化"},
    "S04_quota_failover": {"succ_pct": 99, "p99_ms": 1500, "label": "配额耗尽与恢复"},
    "S05_quality_penalty": {"succ_pct": 97, "p99_ms": 5000, "label": "延迟/质量降权"},
    "S06_mixed_fault": {"succ_pct": 94, "p99_ms": 5000, "label": "混合故障韧性"},
    "S07_peak_dispatch": {"succ_pct": 99, "p99_ms": 2500, "label": "高峰动态调度"},
    "S08_sticky": {"succ_pct": 95, "p99_ms": 1500, "label": "Sticky 连续性"},
    "S09_streaming": {"succ_pct": 90, "p99_ms": 1500, "label": "流式 SSE"},
    "S10_long_prompt": {"succ_pct": 92, "p99_ms": 2500, "label": "长 Prompt"},
    "S11_quota_recovery": {"succ_pct": 99, "p99_ms": 1500, "label": "周期性配额恢复"},
    "S12_comprehensive": {"succ_pct": 98, "p99_ms": 5000, "label": "综合压测"},
    "S13_no_candidate": {"succ_pct": 0, "p99_ms": 5000, "label": "无可用节点", "fail_expected": True},
    "S14_model_not_found": {"succ_pct": 0, "p99_ms": 100, "label": "模型不存在", "fail_expected": True},
    "S15_cross_group_failover": {"succ_pct": 99, "p99_ms": 3000, "label": "跨组故障迁移"},
    "S16_before_recharge": {"succ_pct": 99, "p99_ms": 2500, "label": "配额恢复前"},
    "S16_after_recharge": {"succ_pct": 99, "p99_ms": 2500, "label": "配额恢复后"},
    "S17_stream_continuation": {"label": "流式断连续传"},
    "S18_null_handling": {"label": "空值/边界请求"},
    "S19_tenant_isolation": {"label": "多租户隔离"},
    "S20_auto_title": {"label": "自动标题"},
    "S21_branch_session": {"label": "分支会话"},
    "S22_instant_summary": {"label": "即时总结"},
    "S23_long_text_chunked": {"label": "长文本压缩"},
    "C01_concurrency": {"label": "并发阶梯"},
    "P01_performance": {"label": "性能基线"},
    "R01_reliability": {"label": "可靠性与恢复"},
}


def _legacy_evaluate(name: str, data: dict[str, Any]) -> tuple[str, str]:
    gate = GATES.get(name)
    if not gate:
        return "INVALID", "unknown scenario"
    metrics = data.get("metrics") or {}
    pct = float(metrics.get("success_rate", 0)) * 100
    p99 = float(metrics.get("p99_ms", 999999))
    expected_failure = bool(gate.get("fail_expected"))
    pct_ok = pct <= gate["succ_pct"] if expected_failure else pct >= gate.get("succ_pct", 0)
    p99_ok = p99 <= gate.get("p99_ms", 999999)
    return ("PASS" if pct_ok and p99_ok else "FAIL"), (
        f"legacy success_rate={pct:.1f}% p99={p99:.0f}ms"
    )


def _evaluate_gates(name: str, data: dict[str, Any]) -> tuple[bool, list[str]]:
    gate = GATES.get(name)
    if not gate:
        return True, []
    metrics = data.get("metrics") or {}
    failures: list[str] = []
    success_pct = float(metrics.get("success_rate", 0)) * 100
    p99_ms = float(metrics.get("p99_ms", 999999))
    if gate.get("fail_expected"):
        if success_pct > gate.get("succ_pct", 0):
            failures.append(f"success_rate={success_pct:.1f}% exceeds expected maximum")
    elif "succ_pct" in gate and success_pct < gate["succ_pct"]:
        failures.append(f"success_rate={success_pct:.1f}% below {gate['succ_pct']}%")
    if "p99_ms" in gate and p99_ms > gate["p99_ms"]:
        failures.append(f"p99_ms={p99_ms:.0f} exceeds {gate['p99_ms']}ms")
    if name == "S09_streaming":
        completion = float(metrics.get("stream_completion_rate", 0)) * 100
        if metrics.get("stream_total", 0) and completion < 95:
            failures.append(f"stream_completion_rate={completion:.1f}% below 95%")
    return not failures, failures


def evaluate_file(path: str, expected: str | None = None) -> dict[str, Any]:
    name = os.path.splitext(os.path.basename(path))[0]
    try:
        with open(path, encoding="utf-8") as stream:
            data = json.load(stream)
    except (OSError, json.JSONDecodeError) as exc:
        return {"scenario": name, "status": "INVALID", "reason": f"parse error: {exc}"}

    errors = validate_result(data, expected)
    if errors:
        data = normalize_legacy_result(data, expected or name)
        errors = validate_result(data, expected)
    if errors:
        return {"scenario": name, "status": "INVALID", "reason": "; ".join(errors)}

    status = data["status"]
    checks = data.get("checks") or {}
    false_checks = [key for key, value in checks.items() if value is False]
    failures = data.get("failures") or []
    gate_ok, gate_failures = _evaluate_gates(data["scenario"], data)
    if status == "PASS" and (false_checks or failures or not gate_ok):
        failures = failures + gate_failures
        return {
            "scenario": name,
            "status": "FAIL",
            "run_id": data.get("run_id"),
            "category": data.get("category"),
            "checks": checks,
            "metrics": data.get("metrics") or {},
            "reason": "; ".join(
                [f"false checks: {', '.join(false_checks)}" if false_checks else ""]
                + failures
                + gate_failures
            ).strip("; "),
            "failures": failures,
        }
    if status not in STATUSES:
        return {"scenario": name, "status": "INVALID", "reason": "invalid status"}
    return {
        "scenario": name,
        "status": status,
        "run_id": data.get("run_id"),
        "category": data.get("category"),
        "checks": checks,
        "metrics": data.get("metrics") or {},
        "reason": data.get("reason", ""),
        "failures": failures,
    }


def load_manifest(path: str | None) -> list[str] | None:
    if not path:
        return None
    try:
        with open(path, encoding="utf-8") as stream:
            manifest = json.load(stream)
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"invalid manifest: {exc}") from exc
    scenarios = manifest.get("scenarios") if isinstance(manifest, dict) else None
    if not isinstance(scenarios, list) or not all(isinstance(item, str) for item in scenarios):
        raise ValueError("manifest.scenarios must be an array of names")
    return scenarios


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--results", default="./results")
    parser.add_argument("--manifest", help="JSON manifest containing declared scenario names")
    parser.add_argument("--format", choices=["md", "json"], default="md")
    parser.add_argument("--allow-skipped", action="store_true")
    args = parser.parse_args()

    try:
        declared = load_manifest(args.manifest)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    if declared is None:
        files = sorted(glob.glob(os.path.join(args.results, "*.json")))
        expected_by_file = {path: None for path in files}
    else:
        expected_by_file = {
            os.path.join(args.results, f"{scenario}.json"): scenario for scenario in declared
        }

    rows: list[dict[str, Any]] = []
    for path, expected in expected_by_file.items():
        if not os.path.exists(path):
            rows.append({
                "scenario": expected or os.path.basename(path),
                "status": "INVALID",
                "reason": "missing result file",
            })
            continue
        rows.append(evaluate_file(path, expected))

    counts = {status: sum(1 for row in rows if row["status"] == status) for status in STATUSES}
    blocking = counts["FAIL"] + counts["INVALID"]
    incomplete = counts["SKIPPED"] + counts["BLOCKED_ENVIRONMENT"]
    if blocking:
        overall = "FAIL"
    elif incomplete and not args.allow_skipped:
        overall = "INCOMPLETE"
    else:
        overall = "PASS"

    report = {
        "schema_version": "1.0",
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "results_dir": os.path.abspath(args.results),
        "overall_status": overall,
        "counts": counts,
        "rows": rows,
        "allow_skipped": args.allow_skipped,
    }
    if args.format == "json":
        print(json.dumps(report, ensure_ascii=False, indent=2))
    else:
        print("# LLM Gateway 严格测试报告\n")
        print(f"- 生成时间: {report['generated_at']}")
        print(f"- 结果目录: `{report['results_dir']}`")
        print(f"- 总体状态: **{overall}**")
        print(
            "- 统计: "
            + ", ".join(f"{status}={counts[status]}" for status in sorted(counts))
            + "\n"
        )
        print("## 场景明细\n")
        print("| 状态 | 场景 | 类别 | Run ID | 说明 |")
        print("|---|---|---|---|---|")
        for row in rows:
            print(
                f"| {row['status']} | `{row['scenario']}` | "
                f"{row.get('category', '_')} | `{row.get('run_id', '-')}` | "
                f"{row.get('reason', '')} |"
            )
        if overall != "PASS":
            print("\n## 验收阻断原因\n")
            for row in rows:
                if row["status"] != "PASS":
                    print(f"- `{row['scenario']}`: {row['status']} — {row.get('reason', '')}")

    return 0 if overall == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
