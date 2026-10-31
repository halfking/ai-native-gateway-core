#!/usr/bin/env python3
"""Measure AL口径: via_gateway - direct_upstream.

Callers: run_smoke.sh; operators for G1. Refuses blocked production hosts.
Report JSON fields: timestamp, caliber=AL, direct_p50/p95_ms, via_p50/p95_ms,
added_p50/p95_ms, n. User instruction: scripts/bench/latency only; no prod.
"""

from __future__ import annotations

import argparse
import json
import os
import statistics
import sys
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime, timezone
from typing import List, Tuple
from urllib.parse import urlparse

BLOCKED_HOSTS = {
    "14.103.112.184",
    "14.103.174.71",
    "120.78.136.252",
    "47.97.111.154",
    "120.79.47.154",
    "8.136.114.245",
    "llm.itestu.cn",
    "llm.kxpms.cn",
}


def _host(url: str) -> str:
    return (urlparse(url).hostname or "").lower()


def assert_safe(url: str, label: str) -> None:
    h = _host(url)
    if not h:
        raise SystemExit(f"[latency-bench] FAIL: {label} missing host: {url}")
    if h in BLOCKED_HOSTS or any(h.endswith("." + b) for b in BLOCKED_HOSTS):
        raise SystemExit(f"[latency-bench] REFUSED: {label}={url} blocked host {h}")
    parts = h.split(".")
    if len(parts) == 4 and all(p.isdigit() for p in parts):
        a, b = int(parts[0]), int(parts[1])
        private = (
            a == 127
            or a == 10
            or (a == 192 and b == 168)
            or (a == 172 and 16 <= b <= 31)
        )
        if not private:
            raise SystemExit(f"[latency-bench] REFUSED: {label}={url} public IP")


def percentile(sorted_vals: List[float], p: float) -> float:
    if not sorted_vals:
        return 0.0
    if len(sorted_vals) == 1:
        return sorted_vals[0]
    k = (len(sorted_vals) - 1) * (p / 100.0)
    f = int(k)
    c = min(f + 1, len(sorted_vals) - 1)
    if f == c:
        return sorted_vals[f]
    return sorted_vals[f] + (sorted_vals[c] - sorted_vals[f]) * (k - f)


def one_request(url: str, api_key: str, payload: bytes, timeout: float) -> Tuple[bool, float, str]:
    headers = {"Content-Type": "application/json"}
    if api_key:
        headers["Authorization"] = f"Bearer {api_key}"
    req = urllib.request.Request(url, data=payload, headers=headers, method="POST")
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            _ = resp.read()
            ms = (time.perf_counter() - t0) * 1000.0
            return True, ms, ""
    except Exception as e:  # noqa: BLE001
        ms = (time.perf_counter() - t0) * 1000.0
        return False, ms, str(e)


def run_batch(
    url: str, api_key: str, payload: bytes, n: int, concurrency: int, timeout: float
) -> List[float]:
    latencies: List[float] = []
    errors = 0
    with ThreadPoolExecutor(max_workers=max(1, concurrency)) as ex:
        futs = [ex.submit(one_request, url, api_key, payload, timeout) for _ in range(n)]
        for fut in as_completed(futs):
            ok, ms, _err = fut.result()
            if ok:
                latencies.append(ms)
            else:
                errors += 1
    if errors and not latencies:
        raise SystemExit(f"[latency-bench] all {n} requests failed against {url}")
    return latencies


def main() -> int:
    ap = argparse.ArgumentParser(description="AL口径: via - direct")
    ap.add_argument("--gateway-base", default="")
    ap.add_argument("--upstream-base", default="")
    ap.add_argument("--gateway-api-key", default="")
    ap.add_argument("--upstream-api-key", default="")
    ap.add_argument("--path", default="/v1/chat/completions")
    ap.add_argument("--requests", type=int, default=40)
    ap.add_argument("--concurrency", type=int, default=8)
    ap.add_argument("--timeout", type=float, default=30.0)
    ap.add_argument("--warmup", type=int, default=4)
    ap.add_argument("--output", default="")
    ap.add_argument("--expect-added-p95-max-ms", type=float, default=0.0)
    ap.add_argument("--expect-added-p95-min-ms", type=float, default=0.0)
    args = ap.parse_args()

    gw = (args.gateway_base or os.environ.get("GW_BASE", "")).rstrip("/")
    up = (args.upstream_base or os.environ.get("UPSTREAM_BASE", "")).rstrip("/")
    if not gw or not up:
        print("[latency-bench] need --gateway-base and --upstream-base (or env)", file=sys.stderr)
        return 2
    assert_safe(gw, "gateway-base")
    assert_safe(up, "upstream-base")

    gw_key = args.gateway_api_key or os.environ.get("GATEWAY_API_KEY", "")
    up_key = args.upstream_api_key or os.environ.get("UPSTREAM_API_KEY", "")

    body = {
        "model": "mock-model",
        "messages": [{"role": "user", "content": "ping"}],
        "max_tokens": 8,
        "stream": False,
    }
    payload = json.dumps(body).encode("utf-8")
    gw_url = gw + args.path
    up_url = up + args.path

    if args.warmup > 0:
        run_batch(up_url, up_key, payload, args.warmup, min(2, args.concurrency), args.timeout)
        run_batch(gw_url, gw_key, payload, args.warmup, min(2, args.concurrency), args.timeout)

    direct = sorted(run_batch(up_url, up_key, payload, args.requests, args.concurrency, args.timeout))
    via = sorted(run_batch(gw_url, gw_key, payload, args.requests, args.concurrency, args.timeout))
    n = min(len(direct), len(via))
    added = sorted([max(0.0, via[i] - direct[i]) for i in range(n)])

    report = {
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "caliber": "AL",
        "formula": "via_gateway - direct_upstream (paired samples; floor at 0)",
        "gateway_url": gw_url,
        "upstream_url": up_url,
        "n_direct": len(direct),
        "n_via": len(via),
        "direct_p50_ms": round(percentile(direct, 50), 3),
        "direct_p95_ms": round(percentile(direct, 95), 3),
        "via_p50_ms": round(percentile(via, 50), 3),
        "via_p95_ms": round(percentile(via, 95), 3),
        "added_p50_ms": round(percentile(added, 50), 3) if added else round(max(0.0, percentile(via, 50) - percentile(direct, 50)), 3),
        "added_p95_ms": round(percentile(added, 95), 3) if added else round(max(0.0, percentile(via, 95) - percentile(direct, 95)), 3),
        "direct_mean_ms": round(statistics.fmean(direct), 3) if direct else 0.0,
        "via_mean_ms": round(statistics.fmean(via), 3) if via else 0.0,
        "g1_threshold_ms": 50.0,
    }
    report["g1_pass_hint"] = report["added_p95_ms"] <= 50.0

    text = json.dumps(report, ensure_ascii=False, indent=2)
    print(text)
    if args.output:
        with open(args.output, "w", encoding="utf-8") as f:
            f.write(text + "\n")

    if args.expect_added_p95_max_ms > 0 and report["added_p95_ms"] > args.expect_added_p95_max_ms:
        print(f"[latency-bench] FAIL: added_p95={report['added_p95_ms']} > max {args.expect_added_p95_max_ms}", file=sys.stderr)
        return 1
    if args.expect_added_p95_min_ms > 0 and report["added_p95_ms"] < args.expect_added_p95_min_ms:
        print(f"[latency-bench] FAIL: added_p95={report['added_p95_ms']} < min {args.expect_added_p95_min_ms}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.URLError as e:
        print(f"[latency-bench] URL error: {e}", file=sys.stderr)
        raise SystemExit(1)
