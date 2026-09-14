#!/usr/bin/env python3
"""auto 匹配二轮 E2E 结果分析 —— G1 分类正确率/G4 均衡性/兜底份额/时延汇总。

用法: python3 scripts/audit/auto_matching_round2_analysis.py \
        docs/audit/2026-09-15-auto-matching-e2e-results-v2.jsonl \
        [docs/audit/2026-09-15-auto-matching-e2e-results-v2-suite2.jsonl ...]

只读分析,不改任何状态。每行输入是 cmd/autoroute-e2e-audit 的 JSONL 记录。
"""
import json
import sys
from collections import defaultdict

def load(paths):
    rows = []
    for p in paths:
        for line in open(p):
            line = line.strip()
            if line and not line.startswith("#"):
                rows.append(json.loads(line))
    return rows

def main():
    rows = load(sys.argv[1:])
    total = len(rows)
    errs = [r for r in rows if r.get("error")]
    fails = [r for r in rows if not r.get("error") and not r.get("pass")]
    passes = [r for r in rows if not r.get("error") and r.get("pass")]

    print(f"total={total} pass={len(passes)} fail={len(fails)} error={len(errs)}")
    if total:
        ok = len(passes)
        print(f"classification accuracy (ok/total) = {ok}/{total} = {ok/total:.1%}")

    # 期望分布 vs 实际(混淆视图)
    conf = defaultdict(lambda: defaultdict(int))
    for r in rows:
        if not r.get("error"):
            conf[r.get("expected_task", "?")][r.get("got_task", "?")] += 1
    mis = [(e, g, n) for e, d in conf.items() for g, n in d.items() if e != g and n > 0]
    if mis:
        print("\n== misclassifications (expected -> got: n) ==")
        for e, g, n in sorted(mis, key=lambda x: -x[2]):
            print(f"  {e} -> {g}: {n}")

    # 兜底份额 per task
    fb = defaultdict(lambda: [0, 0])
    lat = defaultdict(list)
    served = defaultdict(lambda: defaultdict(int))
    creds = defaultdict(lambda: defaultdict(int))
    for r in rows:
        if r.get("error"):
            continue
        d = r.get("decision") or {}
        t = d.get("task_type") or r.get("expected_task") or "?"
        fb[t][1] += 1
        if d.get("fallback_used"):
            fb[t][0] += 1
        lat[t].append(r.get("latency_ms") or 0)
        m = r.get("served_model") or d.get("chosen_model") or "?"
        served[t][m] += 1
        if d.get("chosen_credential_id"):
            creds[t][f"{d.get('chosen_model')}@{d.get('chosen_credential_id')}"] += 1

    print("\n== fallback share per task (fb/total) ==")
    for t in sorted(fb):
        n, tot = fb[t]
        lats = sorted(lat[t])
        p50 = lats[len(lats)//2] if lats else 0
        p95 = lats[int(len(lats)*0.95)] if lats else 0
        top = ", ".join(f"{m}×{c}" for m, c in sorted(served[t].items(), key=lambda x: -x[1])[:3])
        print(f"  {t:22} {n}/{tot}  p50={p50}ms p95={p95}ms  served: {top}")

    # G4: chosen composite vs pool top1 (decision 头 candidates_top3[0])
    print("\n== G4 balance: chosen composite vs top3[0] composite ==")
    dev = []
    for r in rows:
        if r.get("error"):
            continue
        d = r.get("decision") or {}
        t3 = d.get("candidates_top3") or []
        if not t3:
            continue
        top1 = t3[0].get("composite_score") or 0
        chosen = next((c.get("composite_score") for c in t3
                       if c.get("model") == d.get("chosen_model")
                       and not d.get("fallback_used")), None)
        if chosen is None:
            chosen = d.get("candidates_top3", [{}])[0].get("composite_score")
        gap = round(top1 - (chosen or 0), 1)
        if gap > 10:
            dev.append((r.get("name"), d.get("task_type"), gap, d.get("fallback_used")))
    if dev:
        for name, t, gap, fbu in dev:
            print(f"  {name:34} {t:20} gap={gap} fb={fbu}")
    else:
        print("  no deviation > 10 (non-fallback) — G4 PASS")

    all_lat = sorted(r.get("latency_ms") or 0 for r in rows if not r.get("error"))
    if all_lat:
        print(f"\nlatency: p50={all_lat[len(all_lat)//2]}ms p95={all_lat[int(len(all_lat)*0.95)]}ms max={all_lat[-1]}ms")

    if errs:
        print("\n== errors ==")
        for r in errs:
            print(f"  {r.get('name')}: {(r.get('error') or '')[:100]}")

if __name__ == "__main__":
    main()
