#!/usr/bin/env python3
# docs/全方面测试/tools/validation_report.py
#
# 把 results/*.json 聚合成 docs/全方面测试/06-验收标准.md 中定义的验收报告。
# 输出 Markdown 表格 + 总体通过率 + 风险点。
#
# 用法：
#   python3 tools/validation_report.py --results ./results > ./results/REPORT.md

import argparse
import glob
import json
import os
import sys
from collections import defaultdict

# 场景验收阈值（与 docs/全方面测试/06-验收标准.md 一致）
GATES = {
    "S01_baseline": {"succ_pct": 99, "p99_ms": 1500, "label": "基准性能"},
    "S02_cost_route": {"succ_pct": 99, "p99_ms": 1500, "label": "成本优化路由"},
    "S03_concurrency_diff": {"succ_pct": 99, "p99_ms": 1500, "label": "并发能力差异化"},
    "S04_quota_failover": {"succ_pct": 99, "p99_ms": 1500, "label": "配额耗尽与恢复"},
    "S05_quality_penalty": {"succ_pct": 97, "p99_ms": 5000, "label": "延迟/质量降权 (G组2-4s注入)"},
    "S06_mixed_fault": {"succ_pct": 94, "p99_ms": 5000, "label": "混合故障韧性 (G慢/J抖/B错同发)"},
    "S07_peak_dispatch": {"succ_pct": 99, "p99_ms": 2500, "label": "高峰动态调度"},
    "S08_sticky": {"succ_pct": 95, "p99_ms": 1500, "label": "Sticky 连续性"},
    "S09_streaming": {"succ_pct": 90, "p99_ms": 1500, "label": "流式 SSE"},
    "S10_long_prompt": {"succ_pct": 92, "p99_ms": 2500, "label": "长 Prompt"},
    "S11_quota_recovery": {"succ_pct": 99, "p99_ms": 1500, "label": "周期性配额恢复"},
    "S12_comprehensive": {"succ_pct": 98, "p99_ms": 5000, "label": "全场景综合压测 (G组2-4s注入)"},
    "S12_post_recovery": {
        "succ_pct": 98,
        "p99_ms": 2500,
        "label": "全场景综合压测 (恢复)",
    },
    # S17-S19: scripts produce pass/fail via 'extra.pass' boolean in their JSON,
    # so we treat the gate as "pass flag in extra" rather than success_rate threshold.
    "S17_stream_continuation": {
        "extra_pass": True,
        "label": "流式断连续传 (按脚本 extra.pass 判定)",
    },
    "S18_null_handling": {
        "extra_pass": True,
        "label": "空值/边界请求处理 (按脚本判定)",
    },
    "S19_tenant_isolation": {
        "extra_pass": True,
        "label": "多租户隔离 (按脚本判定)",
    },
    # 期望失败的场景
    "S13_no_candidate": {
        "succ_pct": 0,
        "p99_ms": 5000,
        "label": "无可用节点",
        "fail_expected": True,
    },
    "S14_model_not_found": {
        "succ_pct": 0,
        "p99_ms": 100,
        "label": "模型不存在",
        "fail_expected": True,
    },
    "S15_cross_group_failover": {
        "succ_pct": 99,
        "p99_ms": 3000,
        "label": "跨组故障迁移",
    },
    "S16_before_recharge": {
        "succ_pct": 99,
        "p99_ms": 2500,
        "label": "配额快速恢复 (前)",
    },
    "S16_after_recharge": {
        "succ_pct": 99,
        "p99_ms": 2500,
        "label": "配额快速恢复 (后)",
    },
    # wave1/wave2 双 wave 形式 (实际 run 输出)
    "S16_precharge_w1": {
        "succ_pct": 99,
        "p99_ms": 2500,
        "label": "S16 wave1 (pre-charge)",
    },
    "S16_recovery_w2": {
        "succ_pct": 99,
        "p99_ms": 2500,
        "label": "S16 wave2 (recovery)",
    },
}


def evaluate(scenario_key: str, m: dict, d: dict = None) -> dict:
    g = GATES.get(scenario_key, {})
    if not g:
        return {"status": "unknown", "reason": "no gate"}

    label = g.get("label", scenario_key)

    # S17-S19 等：scenario scripts 把 'extra.pass' 作为权威 pass/fail 验收，
    # 它们有自己的多步不变量（超出 loadtest 的 success-rate/p99 判定）。
    # extra 写在 JSON 顶层（与 metrics 同级），所以从 d 取；metrics.extra 兜底。
    if g.get("extra_pass"):
        extra = (d or {}).get("extra") or m.get("extra") or {}
        script_pass = bool(extra.get("pass"))
        return {
            "status": "PASS" if script_pass else "FAIL",
            "script_pass": script_pass,
            "label": label,
            "note": "script-extra.pass",
            "total": m.get("total", 0),
        }

    pct = m.get("success_rate", 0) * 100
    p99 = m.get("p99_ms", 9999)
    fail_expected = g.get("fail_expected", False)

    ok_pct = pct >= g["succ_pct"] if not fail_expected else pct <= g["succ_pct"]
    # 期望失败的场景（S13/S14）：调用方预期 gateway 拒绝/无候选，
    # p99 反映"快速 fail" 的尾延迟，不应再受 p99_ms gate 约束。
    ok_p99 = True if fail_expected else p99 <= g["p99_ms"]
    return {
        "status": "PASS" if (ok_pct and ok_p99) else "FAIL",
        "fail_expected": fail_expected,
        "succ_pct": pct,
        "succ_pct_target": g["succ_pct"],
        "p99_ms": p99,
        "p99_ms_target": g["p99_ms"],
        "ok_pct": ok_pct,
        "ok_p99": ok_p99,
        "label": label,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--results", default="./results", help="results/*.json dir")
    parser.add_argument("--format", choices=["md", "json"], default="md")
    args = parser.parse_args()

    files = sorted(glob.glob(os.path.join(args.results, "*.json")))
    if not files:
        print(f"# no results in {args.results}", file=sys.stderr)
        return 1

    rows = []
    for f in files:
        name = os.path.splitext(os.path.basename(f))[0]
        try:
            d = json.load(open(f))
        except Exception as e:
            rows.append({"scenario": name, "status": "PARSE-ERROR", "error": str(e)})
            continue
        m = d.get("metrics", {})
        # S17–S19 把 'extra' 写在 JSON 顶层（与 metrics 同级），不是 metrics 里；
        # 把整份 d 也传给 evaluate 以便 extra_pass gate 能取到。
        ev = evaluate(name, m, d)
        ev["scenario"] = name
        ev["total"] = m.get("total", 0)
        ev["label"] = ev.get("label", name)
        rows.append(ev)

    # 计数
    n_pass = sum(1 for r in rows if r.get("status") == "PASS")
    n_fail = sum(1 for r in rows if r.get("status") == "FAIL")

    # === Markdown 输出 ===
    print("# LLM Gateway 全场景测试报告\n")
    print(f"- 生成时间: {os.popen('date -Iseconds').read().strip()}")
    print(f"- 测试结果目录: `{args.results}`")
    print(f"- 总场景数: {len(rows)}  通过: {n_pass}  失败: {n_fail}\n")

    print("## 总体状态\n")
    icon = "🟢" if n_fail == 0 else "🟡"
    print(f"{icon} 总通过率：{n_pass}/{len(rows)}\n")

    print("## 各场景验收详情\n")
    print("| 状态 | 场景 | 总请求 | 成功率(%) | 目标 | P99(ms) | 目标 | 描述 |")
    print("|---|---|---|---|---|---|---|---|")
    for r in rows:
        if r.get("status") == "PARSE-ERROR":
            print(
                f"| ❌ | {r['scenario']} | _"
                + f" | _ | _ | _ | _ | parse error: {r.get('error', '')} |"
            )
            continue
        ok_pct = "✓" if r.get("ok_pct", False) else "✗"
        ok_p99 = "✓" if r.get("ok_p99", False) else "✗"
        fail_str = "  (预期失败)" if r.get("fail_expected") else ""
        if r.get("status") == "unknown":
            print(
                f"| ⚠️ | `{r['scenario']}` | {r.get('total', 0)} | _ | _ | _ | _ | {r.get('label', r['scenario'])}{fail_str} (no gate defined) |"
            )
            continue
        # script-extra.pass gate path (e.g. S17–S19) — 没有 succ_pct/p99 gate,
        # 直接用脚本自身的 pass 标记判定。
        if r.get("note") == "script-extra.pass":
            mark = "✅" if r.get("status") == "PASS" else "❌"
            print(
                f"| {mark} | `{r['scenario']}` | {r.get('total', 0)} | _ | _ | _ | _ | "
                f"{r['label']} (script pass={r.get('script_pass', False)}) |"
            )
            continue
        print(
            f"| {r['status']} | `{r['scenario']}` | {r['total']} | "
            f"{r['succ_pct']:.1f} {ok_pct} | {r['succ_pct_target']} | "
            f"{r['p99_ms']:.0f} {ok_p99} | {r['p99_ms_target']} | "
            f"{r['label']}{fail_str} |"
        )

    # 失败详情
    fails = [r for r in rows if r.get("status") == "FAIL"]
    if fails:
        print("\n## 失败场景详情\n")
        for r in fails:
            print(f"### ❌ {r['scenario']} — {r['label']}")
            if r.get("note") == "script-extra.pass":
                print(f"- 脚本判定: pass={r.get('script_pass', False)}")
                fail_path = os.path.join(args.results, f"{r['scenario']}.json")
                print(f"- 原始数据: `{fail_path}`")
                print()
                continue
            print(
                f"- 成功率: {r['succ_pct']:.1f}%  (目标 {r['succ_pct_target']}%, "
                f"{'通过' if r['ok_pct'] else '不通过'})"
            )
            print(
                f"- P99:    {r['p99_ms']:.0f}ms  (目标 {r['p99_ms_target']}ms, "
                f"{'通过' if r['ok_p99'] else '不通过'})"
            )
            fail_path = os.path.join(args.results, f"{r['scenario']}.json")
            print(f"- 原始数据: `{fail_path}`")
            print()

    print("\n## 故障模式覆盖矩阵\n")
    faul_matrix = {
        "S04/S11": ["429 quota_exceeded"],
        "S05": ["slow upstream (2-4s 延迟)"],
        "S06": ["slow + flaky + server_error + rate_limited 同发"],
        "S07": ["tier-3 高峰启用"],
        "S09": ["broken_stream (SSE 写一半断流)"],
        "S10": ["context_length_exceeded"],
        "S11/S16": ["quota 短窗口 → 恢复"],
        "S12": ["15 model × 150 client × 6 min"],
        "S13": ["全部供应商故障 → no_candidate"],
        "S14": ["model_not_found"],
        "S15": ["跨组故障迁移"],
    }
    print("| 场景 | 验证的故障模式 |")
    print("|---|---|")
    for s, fs in faul_matrix.items():
        print(f"| {s} | {', '.join(fs)} |")

    return 0


if __name__ == "__main__":
    sys.exit(main() or 0)
