#!/bin/bash
# tests/local/mock-client/orchestrator.py
#
# N 个客户端持续并发 — 每 client 启动一个进程（so torch）
# 或 N 个并发任务。

import argparse
import asyncio
import json
import os
import sys
import time

import aiohttp

ROOT = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, ROOT)
from client import MockClient  # noqa: E402


async def spawn_clients(
    n: int, base_id: str, rps: float, duration: int, gateway: str, api_key: str
):
    """在单进程内起 N 个并发 client 任务 — 简单 + 资源共享。"""
    clients = [
        MockClient(f"{base_id}-{i}", gateway, api_key, rps, duration) for i in range(n)
    ]

    async def aggregate_printer(period=2.0):
        end_at = time.monotonic() + duration
        while time.monotonic() < end_at:
            await asyncio.sleep(period)
            agg = {
                "elapsed": time.monotonic() - (end_at - duration),
                "clients": [c.stats.snapshot() for c in clients],
            }
            # 汇总
            total = sum(c["total"] for c in agg["clients"])
            succ = sum(c["succ"] for c in agg["clients"])
            fail = sum(c["fail"] for c in agg["clients"])
            rate = (succ / total * 100) if total else 0
            print(
                f"\n[{agg['elapsed']:6.1f}s] global: total={total} succ={succ} "
                f"fail={fail} rate={rate:.1f}% N={n} target_rps={rps * n:.0f}",
                flush=True,
            )

    tasks = [c.run() for c in clients]
    tasks.append(aggregate_printer())
    await asyncio.gather(*tasks)

    # 最终汇总
    grand = {
        "total": 0,
        "succ": 0,
        "fail": 0,
        "errors_by_status": {},
        "latencies": [],
    }
    for c in clients:
        s = c.stats.snapshot()
        grand["total"] += s["total"]
        grand["succ"] += s["succ"]
        grand["fail"] += s["fail"]
        for k, v in s["errors_by_status"].items():
            grand["errors_by_status"][k] = grand["errors_by_status"].get(k, 0) + v
        grand["latencies"].extend(c.stats.latencies)

    if grand["latencies"]:
        n = len(grand["latencies"])
        lats = sorted(grand["latencies"])
        p50 = lats[n // 2]
        p99 = lats[int(n * 0.99)]
        grand["p50_ms"] = p50 * 1000
        grand["p99_ms"] = p99 * 1000

    print("\n=== FINAL ===", flush=True)
    print(json.dumps(grand, indent=2, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--n", type=int, default=10, help="客户端数")
    parser.add_argument("--rps-per-client", type=float, default=5.0)
    parser.add_argument("--duration", type=int, default=60)
    parser.add_argument(
        "--gateway", default=os.getenv("GATEWAY_URL", "http://localhost:58781")
    )
    parser.add_argument(
        "--api-key", default=os.getenv("TEST_API_KEY", "sk-loc-1234567890abcdef")
    )
    parser.add_argument("--base-id", default="orch")
    args = parser.parse_args()

    print(
        f"Starting orchestrator: N={args.n} rps_each={args.rps_per_client} "
        f"duration={args.duration}s target_total_rps={args.n * args.rps_per_client:.0f}"
    )
    try:
        asyncio.run(
            spawn_clients(
                args.n,
                args.base_id,
                args.rps_per_client,
                args.duration,
                args.gateway,
                args.api_key,
            )
        )
    except KeyboardInterrupt:
        print("interrupted")


if __name__ == "__main__":
    main()
