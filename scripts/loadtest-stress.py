#!/usr/bin/env python3
"""Stress test runner — 20+ concurrent clients with sticky tracking.

Tests gateway routing, failover, and sticky session behavior under load.
"""

import argparse
import asyncio
import aiohttp
import time
import json
from collections import Counter
from dataclasses import dataclass, field
from typing import Optional

DEFAULT_MOCK_URLS = [
    "http://localhost:19080",
    "http://localhost:19081",
    "http://localhost:19082",
    "http://localhost:19083",
]
TEST_MODEL = "loadtest-fake-gpt-4o"


@dataclass
class RequestResult:
    session_id: str
    round_num: int
    success: bool
    status: int
    mock_identity: Optional[str] = None
    latency_ms: float = 0.0
    error: Optional[str] = None


@dataclass
class SessionStats:
    session_id: str
    results: list = field(default_factory=list)
    first_mock: Optional[str] = None
    sticky_hits: int = 0
    sticky_misses: int = 0
    errors: int = 0
    total_latency_ms: float = 0.0

    @property
    def total(self):
        return len(self.results)

    @property
    def sticky_accuracy(self):
        ts = self.sticky_hits + self.sticky_misses
        return self.sticky_hits / ts if ts > 0 else 1.0

    @property
    def avg_latency(self):
        return self.total_latency_ms / self.total if self.total else 0.0


async def session_worker(
    session_id,
    rounds,
    mock_urls,
    model,
    gateway_url,
    api_key,
    sem,
    session_stats,
    inject_faults=False,
):
    timeout = aiohttp.ClientTimeout(total=30, connect=5)
    for r in range(rounds):
        async with sem:
            try:
                if inject_faults and r > 0 and r % 10 == 0:
                    port = 19080 + (r % 4)
                    mode = ["slow", "server_error", "flaky"][r % 3]
                    try:
                        async with aiohttp.ClientSession() as s:
                            await s.post(
                                f"http://localhost:{port}/admin/state",
                                json={"mode": mode, "ttl_seconds": 5},
                                timeout=aiohttp.ClientTimeout(total=2),
                            )
                    except Exception:
                        pass
                payload = {
                    "model": model,
                    "messages": [
                        {"role": "user", "content": f"r{r}-s{session_id[-6:]}"}
                    ],
                }
                headers = {
                    "Content-Type": "application/json",
                    "X-Gw-Session-Id": session_id,
                }
                if gateway_url:
                    url = f"{gateway_url}/v1/chat/completions"
                    if api_key:
                        headers["Authorization"] = f"Bearer {api_key}"
                else:
                    url = f"{mock_urls[r % len(mock_urls)]}/v1/chat/completions"
                started = time.monotonic()
                status = 0
                body = {}
                try:
                    async with aiohttp.ClientSession(timeout=timeout) as session:
                        async with session.post(
                            url, json=payload, headers=headers
                        ) as resp:
                            latency = (time.monotonic() - started) * 1000
                            status = resp.status
                            buf = []
                            try:
                                async for chunk in resp.content.iter_any():
                                    buf.append(chunk)
                            except Exception:
                                pass
                            raw = b"".join(buf)
                            if not raw:
                                try:
                                    raw = await resp.read()
                                except Exception:
                                    raw = b""
                            if raw:
                                try:
                                    body = json.loads(raw)
                                except json.JSONDecodeError:
                                    text = raw.decode("utf-8", errors="ignore")
                                    lb = max(text.rfind("}"), text.rfind("]"))
                                    if lb > 0:
                                        try:
                                            body = json.loads(text[: lb + 1])
                                        except Exception:
                                            body = {}
                except (aiohttp.ClientPayloadError, Exception):
                    latency = (time.monotonic() - started) * 1000
                    status = 200
                mock_id = None
                if isinstance(body, dict):
                    mock_id = body.get("_mock_identity")
                    if not mock_id:
                        try:
                            content = body["choices"][0]["message"]["content"]
                            if "[mock=" in content:
                                mock_id = (
                                    content.split("[mock=")[1].split("]")[0].strip()
                                )
                        except Exception:
                            pass
                result = RequestResult(
                    session_id=session_id,
                    round_num=r,
                    success=200 <= status < 300 and mock_id is not None,
                    status=status,
                    mock_identity=mock_id,
                    latency_ms=latency,
                    error=None if status < 400 else str(body.get("error", {}))[:100],
                )
                session_stats.results.append(result)
                session_stats.total_latency_ms += latency
                if session_stats.first_mock is None and mock_id:
                    session_stats.first_mock = mock_id
                if mock_id and session_stats.first_mock:
                    if mock_id == session_stats.first_mock:
                        session_stats.sticky_hits += 1
                    else:
                        session_stats.sticky_misses += 1
                if not result.success:
                    session_stats.errors += 1
            except Exception:
                session_stats.errors += 1


async def run_stress_test(
    clients, rounds, mock_urls, model, gateway_url, api_key, inject_faults
):
    sem = asyncio.Semaphore(clients)
    sessions = {}
    print(f"\n🚀 {clients} clients × {rounds} rounds = {clients * rounds} requests")
    print(f"   mode: {'gateway' if gateway_url else 'direct'} | model: {model}")
    started = time.monotonic()
    tasks = []
    for c in range(clients):
        sid = f"client-{c:03d}-{int(started)}"
        sessions[sid] = SessionStats(session_id=sid)
        tasks.append(
            session_worker(
                session_id=sid,
                rounds=rounds,
                mock_urls=mock_urls,
                model=model,
                gateway_url=gateway_url,
                api_key=api_key,
                sem=sem,
                session_stats=sessions[sid],
                inject_faults=inject_faults,
            )
        )
    await asyncio.gather(*tasks)
    elapsed = time.monotonic() - started

    total = sum(s.total for s in sessions.values())
    errors = sum(s.errors for s in sessions.values())
    sh = sum(s.sticky_hits for s in sessions.values())
    sm = sum(s.sticky_misses for s in sessions.values())
    total_sticky = sh + sm
    latencies = sorted([r.latency_ms for s in sessions.values() for r in s.results])
    print(f"\n📊 Report ({elapsed:.1f}s, {total / max(elapsed, 0.001):.0f} req/s)")
    print(f"   total={total}  success={total - errors}  errors={errors}")
    if latencies:
        p50 = latencies[len(latencies) // 2]
        p95 = latencies[int(len(latencies) * 0.95)] if len(latencies) > 1 else p50
        p99 = latencies[int(len(latencies) * 0.99)] if len(latencies) > 1 else p95
        print(f"   P50={p50:.0f}ms  P95={p95:.0f}ms  P99={p99:.0f}ms")
    if total_sticky > 0:
        print(f"   Sticky: {sh}/{total_sticky} = {sh * 100 / total_sticky:.1f}%")
    mock_dist = Counter()
    for s in sessions.values():
        for r in s.results:
            if r.mock_identity:
                mock_dist[r.mock_identity] += 1
    print(f"   Mock distribution: {dict(sorted(mock_dist.items()))}")
    sr = (total - errors) * 100 / max(total, 1)
    sk = sh * 100 / max(total_sticky, 1) if total_sticky > 0 else 100
    status = "✅ PASS" if sr >= 95 and sk >= 70 else "⚠️ REVIEW"
    print(f"   {status}  success={sr:.1f}%  sticky={sk:.1f}%")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--clients", type=int, default=20)
    p.add_argument("--rounds", type=int, default=50)
    p.add_argument("--mode", choices=["direct", "gateway"], default="direct")
    p.add_argument("--mocks", default=",".join(DEFAULT_MOCK_URLS))
    p.add_argument("--gateway", default=None)
    p.add_argument("--api-key", default=None)
    p.add_argument("--model", default=TEST_MODEL)
    p.add_argument("--fault-injection", action="store_true")
    args = p.parse_args()
    mock_urls = [u.strip() for u in args.mocks.split(",") if u.strip()]
    asyncio.run(
        run_stress_test(
            clients=args.clients,
            rounds=args.rounds,
            mock_urls=mock_urls,
            model=args.model,
            gateway_url=args.gateway if args.mode == "gateway" else None,
            api_key=args.api_key,
            inject_faults=args.fault_injection,
        )
    )


if __name__ == "__main__":
    main()
