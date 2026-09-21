#!/usr/bin/env python3
"""auto 匹配二轮 E2E 结果分析。

该脚本只分析 cmd/autoroute-e2e-audit 写出的响应记录，不能替代生产 252
候选池全量重算、价格审计或 node_probe_state/decision_trace 持久化核验。

用法:
    python3 scripts/audit/auto_matching_round2_analysis.py \
        docs/audit/2026-09-15-auto-matching-e2e-results-v2.jsonl \
        [docs/audit/2026-09-15-auto-matching-e2e-results-v2-suite2.jsonl ...]
"""

import json
import sys
from collections import Counter, defaultdict


def load(paths):
    rows = []
    for path in paths:
        with open(path, encoding="utf-8") as source:
            for line in source:
                line = line.strip()
                if line and not line.startswith("#"):
                    rows.append(json.loads(line))
    return rows


def percentile(values, percentile_value):
    if not values:
        return None
    values = sorted(values)
    return values[min(len(values) - 1, int(len(values) * percentile_value))]


def main():
    rows = load(sys.argv[1:])
    total = len(rows)
    errors = [row for row in rows if row.get("error")]
    comparable = [row for row in rows if not row.get("error")]
    correct = [row for row in comparable if row.get("pass")]
    incorrect = [row for row in comparable if not row.get("pass")]

    print("== request delivery and classification evidence ==")
    print(f"total_requests={total}")
    print(f"transport_or_dispatch_errors={len(errors)}")
    print(f"completed_requests={len(comparable)}")
    print(f"completed_request_rate={len(comparable)}/{total} = {len(comparable)/total:.1%}" if total else "completed_request_rate=n/a")
    print(f"classification_correct_on_completed={len(correct)}/{len(comparable)} = {len(correct)/len(comparable):.1%}" if comparable else "classification_correct_on_completed=n/a")
    print(f"end_to_end_correct_delivery={len(correct)}/{total} = {len(correct)/total:.1%}" if total else "end_to_end_correct_delivery=n/a")
    print("note: classification accuracy excludes rows that never produced a decision; "
          "end_to_end_correct_delivery includes them and is the availability-inclusive measure.")

    confusion = defaultdict(Counter)
    for row in comparable:
        confusion[row.get("expected_task", "?")][row.get("got_task", "?")] += 1
    mismatches = [
        (expected, actual, count)
        for expected, actuals in confusion.items()
        for actual, count in actuals.items()
        if expected != actual and count
    ]
    if mismatches:
        print("\n== classification mismatches among completed requests ==")
        for expected, actual, count in sorted(mismatches, key=lambda item: -item[2]):
            print(f"  {expected} -> {actual}: {count}")

    fallback = defaultdict(lambda: [0, 0])
    latency = defaultdict(list)
    served = defaultdict(Counter)
    for row in comparable:
        decision = row.get("decision") or {}
        task = decision.get("task_type") or row.get("expected_task") or "?"
        fallback[task][1] += 1
        if decision.get("fallback_used"):
            fallback[task][0] += 1
        if row.get("latency_ms") is not None:
            latency[task].append(row["latency_ms"])
        model = row.get("served_model") or decision.get("chosen_model") or "?"
        served[task][model] += 1

    fallback_count = sum(values[0] for values in fallback.values())
    fallback_total = sum(values[1] for values in fallback.values())
    print("\n== fallback share among completed requests ==")
    print(f"overall_fallback={fallback_count}/{fallback_total} = {fallback_count/fallback_total:.1%}" if fallback_total else "overall_fallback=n/a")
    for task in sorted(fallback):
        used, task_total = fallback[task]
        top_models = ", ".join(f"{model}×{count}" for model, count in served[task].most_common(3))
        p50 = percentile(latency[task], 0.50)
        p95 = percentile(latency[task], 0.95)
        print(f"  {task:22} {used}/{task_total}  p50={p50}ms p95={p95}ms  served: {top_models}")

    # The response header only exposes candidates_top3. It cannot prove the
    # database-wide candidate-pool top1 or validate prices missing from CMI.
    # Compare only non-fallback selections explicitly represented in that
    # response-header window.
    window_checked = 0
    missing_chosen_score = 0
    fallback_skipped = 0
    gaps = []
    for row in comparable:
        decision = row.get("decision") or {}
        candidates = decision.get("candidates_top3") or []
        if not candidates:
            continue
        if decision.get("fallback_used"):
            fallback_skipped += 1
            continue
        selected = next(
            (candidate for candidate in candidates if candidate.get("model") == decision.get("chosen_model")),
            None,
        )
        if selected is None:
            missing_chosen_score += 1
            continue
        window_checked += 1
        top_score = candidates[0].get("composite_score") or 0
        selected_score = selected.get("composite_score") or 0
        gap = round(top_score - selected_score, 1)
        if gap > 10:
            gaps.append((row.get("name"), decision.get("task_type"), gap))

    print("\n== G4 response-header top-3 window evidence ==")
    print("scope: non-fallback decisions whose chosen model appears in X-Gw-Auto-Decision candidates_top3; "
          "this is not a production-252 full candidate-pool or pricing recomputation.")
    print(f"window_checked={window_checked} fallback_skipped={fallback_skipped} chosen_not_in_top3={missing_chosen_score}")
    if gaps:
        print("result: deviations >10 in the exposed top-3 window")
        for name, task, gap in gaps:
            print(f"  {name:34} {task:20} gap={gap}")
    else:
        print("result: no deviation >10 in the exposed top-3 window")

    all_latency = [row.get("latency_ms") for row in comparable if row.get("latency_ms") is not None]
    if all_latency:
        print("\n== latency among completed requests ==")
        print(f"p50={percentile(all_latency, 0.50)}ms p95={percentile(all_latency, 0.95)}ms max={max(all_latency)}ms")

    if errors:
        print("\n== transport or dispatch errors (excluded from classification denominator) ==")
        for row in errors:
            print(f"  {row.get('name')}: {(row.get('error') or '')[:160]}")


if __name__ == "__main__":
    main()
