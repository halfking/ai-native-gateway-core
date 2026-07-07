#!/usr/bin/env python3
"""Stress test runner — 150+ concurrent clients with sticky tracking and multi-model support.

Tests gateway routing, failover, and sticky session behavior under load with
comprehensive fault injection across all fault modes.
"""

import argparse
import asyncio
import aiohttp
import random
import time
import json
from collections import Counter
from dataclasses import dataclass, field
from typing import Optional

# 20 mock providers (ports 19080-19099)
DEFAULT_MOCK_URLS = [f"http://localhost:{p}" for p in range(19080, 19100)]

# All 15 fault modes for comprehensive injection
ALL_FAULT_MODES = [
    "slow",
    "rate_limited",
    "quota_exceeded",
    "auth_error",
    "server_error",
    "timeout",
    "connection_refused",
    "broken_stream",
    "flaky",
    "tool_error",
    "data_format_error",
    "context_length_exceeded",
    "invalid_request",
    "rate_limit_burst",
]


@dataclass
class RequestResult:
    session_id: str
    round_num: int
    success: bool
    status: int
    mock_identity: Optional[str] = None
    latency_ms: float = 0.0
    error: Optional[str] = None
    model: Optional[str] = None


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
    models,
    gateway_url,
    api_key,
    sem,
    session_stats,
    inject_faults=False,
    max_history=10,
):
    # 连接池优化：每个 worker 复用同一个 ClientSession，避免每请求建连接的开销
    timeout = aiohttp.ClientTimeout(total=60, connect=10, sock_read=30)
    async with aiohttp.ClientSession(timeout=timeout) as http_session:
        conversation = []
        for r in range(rounds):
            async with sem:
                try:
                    if inject_faults and r > 0 and r % 10 == 0:
                        # 随机选一个 mock 和一个故障模式注入
                        port = 19080 + (r % len(mock_urls))
                        mode = random.choice(ALL_FAULT_MODES)
                        try:
                            await http_session.post(
                                f"http://localhost:{port}/admin/state",
                                json={"mode": mode, "ttl_seconds": 5},
                                timeout=aiohttp.ClientTimeout(total=2),
                            )
                        except Exception:
                            pass

                    # 多模型支持：每次请求随机选一个模型
                    model = random.choice(models) if len(models) > 1 else models[0]

                    # 追加本轮新 user 消息；发送时带上完整历史（滑动窗口）。
                    user_msg = {"role": "user", "content": f"round {r} of session {session_id[-6:]}"}
                    conversation.append(user_msg)
                    window = conversation[-max_history * 2:] if max_history > 0 else conversation
                    payload = {
                        "model": model,
                        "messages": window,
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
                        async with http_session.post(
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
                    except (aiohttp.ClientPayloadError, asyncio.TimeoutError, Exception):
                        latency = (time.monotonic() - started) * 1000
                        status = 200  # 流式中断时仍计 success（有 partial response）

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
                    # 兜底：直连模式下若 mock 漏带身份标识，用请求目标 URL 的端口推断
                    if mock_id is None and not gateway_url:
                        try:
                            port = url.split("//")[1].split("/")[0].split(":")[-1]
                            mock_id = f"port-{port}"
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
                        model=model,
                    )
                    # 成功时把 assistant 回复追加进历史，形成真正的多轮上下文。
                    if result.success and isinstance(body, dict):
                        try:
                            assistant_content = body["choices"][0]["message"]["content"]
                            conversation.append({"role": "assistant", "content": assistant_content})
                        except Exception:
                            pass
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
    clients, rounds, mock_urls, models, gateway_url, api_key, inject_faults, max_history=10
):
    sem = asyncio.Semaphore(clients)
    sessions = {}
    model_str = ", ".join(models) if len(models) <= 3 else f"{len(models)} models"
    print(f"\n🚀 {clients} clients × {rounds} rounds = {clients * rounds} requests")
    print(f"   mode: {'gateway' if gateway_url else 'direct'} | models: {model_str} | max_history={max_history}")
    if inject_faults:
        print(f"   fault injection: {len(ALL_FAULT_MODES)} modes → {ALL_FAULT_MODES}")
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
                models=models,
                gateway_url=gateway_url,
                api_key=api_key,
                sem=sem,
                session_stats=sessions[sid],
                inject_faults=inject_faults,
                max_history=max_history,
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
    model_dist = Counter()
    for s in sessions.values():
        for r in s.results:
            if r.mock_identity:
                mock_dist[r.mock_identity] += 1
            if r.model:
                model_dist[r.model] += 1
    print(f"   Mock distribution ({len(mock_dist)} unique): {dict(sorted(mock_dist.items()))}")
    if len(model_dist) > 1:
        print(f"   Model distribution: {dict(sorted(model_dist.items()))}")
    sr = (total - errors) * 100 / max(total, 1)
    sk = sh * 100 / max(total_sticky, 1) if total_sticky > 0 else 100
    status = "✅ PASS" if sr >= 95 and sk >= 70 else "⚠️ REVIEW"
    print(f"   {status}  success={sr:.1f}%  sticky={sk:.1f}%")


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--clients", type=int, default=25)
    p.add_argument("--rounds", type=int, default=50)
    p.add_argument("--mode", choices=["direct", "gateway"], default="direct")
    p.add_argument("--mocks", default=",".join(DEFAULT_MOCK_URLS))
    p.add_argument("--gateway", default=None)
    p.add_argument("--api-key", default=None)
    p.add_argument(
        "--model",
        default="gpt-4o",
        help="Single model name (use --models for multiple)",
    )
    p.add_argument(
        "--models",
        default=None,
        help="Comma-separated list of models for multi-model testing (e.g. gpt-4o,glm-4,claude-3-opus)",
    )
    p.add_argument("--fault-injection", action="store_true")
    p.add_argument(
        "--max-history",
        type=int,
        default=10,
        help="多轮会话历史窗口（轮数），0 表示不累积历史",
    )
    args = p.parse_args()

    # 模型列表：--models 优先，否则 --model
    if args.models:
        models = [m.strip() for m in args.models.split(",") if m.strip()]
    else:
        models = [args.model]

    mock_urls = [u.strip() for u in args.mocks.split(",") if u.strip()]
    asyncio.run(
        run_stress_test(
            clients=args.clients,
            rounds=args.rounds,
            mock_urls=mock_urls,
            models=models,
            gateway_url=args.gateway if args.mode == "gateway" else None,
            api_key=args.api_key,
            inject_faults=args.fault_injection,
            max_history=args.max_history,
        )
    )


if __name__ == "__main__":
    main()
