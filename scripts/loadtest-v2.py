#!/usr/bin/env python3
"""
Enhanced loadtest runner v2 — multi-dimensional stress testing.

Adds on top of loadtest-stress.py:
- Multi API key support (--api-keys, rotation/weighted distribution)
- Session pool with reuse ratio (--sessions, --session-reuse-ratio)
- Variable prompt sizes (--prompt-size short|medium|long|mixed)
- Streaming support (--stream)
- JSON result persistence (--output)
- Per-group mock distribution analysis (--profile-config)
"""

import argparse
import asyncio
import json
import math
import os
import random
import time
from collections import Counter, defaultdict
from dataclasses import dataclass, field, asdict
from typing import Optional

import aiohttp

# Mock port range
DEFAULT_MOCK_URLS = [f"http://localhost:{p}" for p in range(19080, 19140)]

# Prompt size presets (approximate token counts)
PROMPT_SIZES = {
    "short": (10, 30),      # ~10-30 tokens
    "medium": (100, 300),   # ~100-300 tokens
    "long": (1000, 3000),   # ~1000-3000 tokens
}

# Reusable text blocks for prompt generation
Lorem = (
    "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
    "Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. "
    "Ut enim ad minim veniam, quis nostrud exercitation ullamco laboris. "
)


def generate_prompt(size_label: str) -> str:
    """Generate a prompt of approximately the requested token size."""
    if size_label == "mixed":
        size_label = random.choice(list(PROMPT_SIZES.keys()))
    lo, hi = PROMPT_SIZES.get(size_label, PROMPT_SIZES["short"])
    target_tokens = random.randint(lo, hi)
    # ~4 chars per token
    target_chars = target_tokens * 4
    # Build by repeating Lorem
    repetitions = max(1, target_chars // len(Lorem) + 1)
    base = (Lorem * repetitions)[:target_chars]
    # Add a question at the end to make it a valid prompt
    questions = [
        "What is the main topic of the above text?",
        "Summarize the key points.",
        "Translate this to formal English.",
        "What are the implications?",
    ]
    return base + " " + random.choice(questions)


@dataclass
class RequestResult:
    session_id: str
    round_num: int
    success: bool
    status: int
    api_key_idx: int = 0
    mock_identity: Optional[str] = None
    latency_ms: float = 0.0
    ttfb_ms: float = 0.0  # time to first byte (stream only)
    error: Optional[str] = None
    model: Optional[str] = None
    stream_complete: bool = True
    prompt_tokens_est: int = 0


@dataclass
class SessionStats:
    session_id: str
    results: list = field(default_factory=list)
    first_mock: Optional[str] = None
    sticky_hits: int = 0
    sticky_misses: int = 0

    @property
    def total(self):
        return len(self.results)

    @property
    def success_count(self):
        return sum(1 for r in self.results if r.success)

    @property
    def sticky_accuracy(self):
        ts = self.sticky_hits + self.sticky_misses
        return self.sticky_hits / ts if ts > 0 else 1.0


def percentile(sorted_list, p):
    """Compute the p-th percentile (0-100) using linear interpolation."""
    if not sorted_list:
        return 0.0
    if len(sorted_list) == 1:
        return sorted_list[0]
    k = (len(sorted_list) - 1) * (p / 100.0)
    f = math.floor(k)
    c = math.ceil(k)
    if f == c:
        return sorted_list[int(k)]
    return sorted_list[f] * (c - k) + sorted_list[c] * (k - f)


async def session_worker(
    worker_id: int,
    rounds: int,
    models: list,
    gateway_url: str,
    api_keys: list,
    session_pool: list,
    session_reuse_ratio: float,
    prompt_size: str,
    use_stream: bool,
    max_history: int,
    sem: asyncio.Semaphore,
    results: list,
    session_stats: dict,
):
    """One client worker that sends rounds requests."""
    timeout = aiohttp.ClientTimeout(total=120, connect=10, sock_read=60)
    async with aiohttp.ClientSession(timeout=timeout) as http_session:
        conversation = []
        for r in range(rounds):
            async with sem:
                # Pick API key (rotate by worker_id + round)
                key_idx = (worker_id + r) % len(api_keys)
                api_key = api_keys[key_idx]

                # Pick session (reuse or new)
                if session_pool and random.random() < session_reuse_ratio:
                    session_id = random.choice(session_pool)
                else:
                    session_id = f"client-{worker_id:03d}-{int(time.time()*1000)}"

                # Pick model
                model = random.choice(models) if len(models) > 1 else models[0]

                # Build message
                prompt = generate_prompt(prompt_size)
                conversation.append({"role": "user", "content": prompt})
                window = conversation[-(max_history * 2):] if max_history > 0 else conversation

                payload = {"model": model, "messages": window}
                if use_stream:
                    payload["stream"] = True

                headers = {
                    "Content-Type": "application/json",
                    "X-Gw-Session-Id": session_id,
                }
                url = f"{gateway_url}/v1/chat/completions"
                if api_key:
                    headers["Authorization"] = f"Bearer {api_key}"

                started = time.monotonic()
                status = 0
                mock_id = None
                stream_complete = True
                ttfb_ms = 0.0
                error = None
                prompt_tokens_est = len(prompt) // 4

                try:
                    async with http_session.post(url, json=payload, headers=headers) as resp:
                        latency = (time.monotonic() - started) * 1000
                        status = resp.status

                        if use_stream and status == 200:
                            # Parse SSE stream
                            first_byte = True
                            buf = []
                            async for raw_chunk in resp.content:
                                if first_byte:
                                    ttfb_ms = (time.monotonic() - started) * 1000
                                    first_byte = False
                                text = raw_chunk.decode("utf-8", errors="replace")
                                buf.append(text)
                            raw = "".join(buf)
                            # Check for stream completion
                            if "[DONE]" not in raw:
                                stream_complete = False
                            # Extract mock identity
                            for line in raw.split("\n"):
                                if line.startswith("data: ") and "[DONE]" not in line:
                                    try:
                                        d = json.loads(line[6:])
                                        delta = d.get("choices", [{}])[0].get("delta", {})
                                        content = delta.get("content", "")
                                        if "[mock=" in content:
                                            mock_id = content.split("[mock=")[1].split("]")[0]
                                    except (json.JSONDecodeError, IndexError):
                                        pass
                        else:
                            # Non-stream: read body
                            raw_bytes = await resp.read()
                            raw = raw_bytes.decode("utf-8", errors="replace")
                            try:
                                body = json.loads(raw)
                                mock_id = body.get("_mock_identity")
                                if not mock_id and "[mock=" in raw:
                                    mock_id = raw.split("[mock=")[1].split("]")[0]
                            except json.JSONDecodeError:
                                if "[mock=" in raw:
                                    mock_id = raw.split("[mock=")[1].split("]")[0]

                        success = 200 <= status < 300 and mock_id is not None
                        if not success and not error:
                            error = raw[:200] if raw else f"status={status}"

                except asyncio.TimeoutError:
                    latency = (time.monotonic() - started) * 1000
                    error = "timeout"
                except Exception as e:
                    latency = (time.monotonic() - started) * 1000
                    error = str(e)[:200]

                result = RequestResult(
                    session_id=session_id,
                    round_num=r,
                    success=success,
                    status=status,
                    api_key_idx=key_idx,
                    mock_identity=mock_id,
                    latency_ms=latency,
                    ttfb_ms=ttfb_ms,
                    error=error,
                    model=model,
                    stream_complete=stream_complete,
                    prompt_tokens_est=prompt_tokens_est,
                )
                results.append(result)

                # Track sticky
                if session_id not in session_stats:
                    session_stats[session_id] = SessionStats(session_id=session_id)
                ss = session_stats[session_id]
                ss.results.append(result)
                if success and mock_id:
                    if ss.first_mock is None:
                        ss.first_mock = mock_id
                    elif mock_id == ss.first_mock:
                        ss.sticky_hits += 1
                    else:
                        ss.sticky_misses += 1


def parse_api_keys(args):
    """Parse API keys from --api-keys (comma-separated) or --api-key (single)."""
    if args.api_keys:
        # Split by comma but be careful with keys that contain commas (unlikely)
        return [k.strip() for k in args.api_keys.split(",") if k.strip()]
    if args.api_key:
        return [args.api_key]
    return [None]


async def run_test(args):
    api_keys = parse_api_keys(args)
    models = args.models.split(",") if args.models else [args.model]
    session_pool = [f"pool-session-{i:03d}" for i in range(args.sessions)] if args.sessions > 0 else []

    sem = asyncio.Semaphore(args.clients)
    results = []
    session_stats = {}

    total = args.clients * args.rounds
    print(f"🚀 {args.clients} clients × {args.rounds} rounds = {total} requests")
    print(f"   gateway={args.gateway} | models={len(models)} | keys={len(api_keys)} | "
          f"sessions={'pool:' + str(len(session_pool)) if session_pool else 'per-client'} | "
          f"prompt={args.prompt_size} | stream={args.stream}")

    start = time.time()
    tasks = [
        session_worker(
            i, args.rounds, models, args.gateway, api_keys,
            session_pool, args.session_reuse_ratio, args.prompt_size,
            args.stream, args.max_history, sem, results, session_stats,
        )
        for i in range(args.clients)
    ]
    await asyncio.gather(*tasks)
    elapsed = time.time() - start

    # ── Compute statistics ──
    total_reqs = len(results)
    successes = [r for r in results if r.success]
    errors = [r for r in results if not r.success]
    latencies = sorted([r.latency_ms for r in successes])
    ttfbs = sorted([r.ttfb_ms for r in successes if r.ttfb_ms > 0])

    mock_dist = Counter(r.mock_identity for r in successes)
    model_dist = Counter(r.model for r in successes)
    key_dist = Counter(r.api_key_idx for r in results)

    # Sticky stats
    total_sticky_hits = sum(s.sticky_hits for s in session_stats.values())
    total_sticky_total = sum(s.sticky_hits + s.sticky_misses for s in session_stats.values())
    sticky_rate = total_sticky_hits / total_sticky_total if total_sticky_total > 0 else 0

    throughput = total_reqs / elapsed if elapsed > 0 else 0

    # Mock group distribution (if profile config available)
    group_dist = {}
    if args.profile_config and os.path.exists(args.profile_config):
        with open(args.profile_config) as f:
            config = json.load(f)
        port_to_group = {}
        for g, ports in config.get("group_port_map", {}).items():
            for p in ports:
                port_to_group[p] = g
        # Map mock_identity (e.g. "mock-00") to port
        for mock_id, count in mock_dist.items():
            # mock-00 → port 19080; mock-NN → port 19080+NN
            try:
                idx = int(mock_id.replace("mock-", ""))
                port = 19080 + idx
                group = port_to_group.get(port, "?")
                group_dist[group] = group_dist.get(group, 0) + count
            except (ValueError, IndexError):
                pass

    summary = {
        "test_config": {
            "clients": args.clients,
            "rounds": args.rounds,
            "total_requests": total_reqs,
            "models": models,
            "num_api_keys": len(api_keys),
            "prompt_size": args.prompt_size,
            "stream": args.stream,
            "max_history": args.max_history,
            "session_reuse_ratio": args.session_reuse_ratio,
        },
        "summary": {
            "total": total_reqs,
            "success": len(successes),
            "errors": len(errors),
            "success_rate": len(successes) / total_reqs if total_reqs > 0 else 0,
            "elapsed_seconds": elapsed,
            "throughput_req_s": throughput,
            "p50_ms": percentile(latencies, 50),
            "p95_ms": percentile(latencies, 95),
            "p99_ms": percentile(latencies, 99),
            "ttfb_p50_ms": percentile(ttfbs, 50) if ttfbs else 0,
            "ttfb_p95_ms": percentile(ttfbs, 95) if ttfbs else 0,
            "sticky_hits": total_sticky_hits,
            "sticky_total": total_sticky_total,
            "sticky_rate": sticky_rate,
            "stream_incomplete": sum(1 for r in successes if not r.stream_complete),
            "unique_mocks": len(mock_dist),
        },
        "mock_distribution": dict(mock_dist.most_common()),
        "model_distribution": dict(model_dist.most_common()),
        "key_distribution": {str(k): v for k, v in sorted(key_dist.items())},
        "group_distribution": dict(sorted(group_dist.items())),
    }

    # ── Print report ──
    print(f"\n📊 Report ({elapsed:.1f}s, {throughput:.0f} req/s)")
    print(f"   total={total_reqs}  success={len(successes)}  errors={len(errors)}")
    p50 = percentile(latencies, 50)
    p95 = percentile(latencies, 95)
    p99 = percentile(latencies, 99)
    print(f"   P50={p50:.0f}ms  P95={p95:.0f}ms  P99={p99:.0f}ms")
    if ttfbs:
        print(f"   TTFB P50={percentile(ttfbs, 50):.0f}ms  P95={percentile(ttfbs, 95):.0f}ms")
    if args.stream:
        incomplete = sum(1 for r in successes if not r.stream_complete)
        print(f"   Stream incomplete: {incomplete}")
    print(f"   Sticky: {total_sticky_hits}/{total_sticky_total} = {sticky_rate*100:.1f}%")
    print(f"   Mock distribution ({len(mock_dist)} unique): {dict(list(mock_dist.most_common(10)))}{'...' if len(mock_dist) > 10 else ''}")
    if group_dist:
        print(f"   Group distribution: {dict(sorted(group_dist.items(), key=lambda x: -x[1]))}")
    if len(model_dist) > 1:
        print(f"   Model distribution: {dict(model_dist.most_common())}")
    if len(key_dist) > 1:
        print(f"   Key distribution: {dict(sorted(key_dist.items()))}")

    # Verdict
    sr = len(successes) / total_reqs if total_reqs > 0 else 0
    verdict = "✅ PASS" if sr >= 0.95 else ("⚠️ REVIEW" if sr >= 0.80 else "❌ FAIL")
    print(f"   {verdict}  success={sr*100:.1f}%  sticky={sticky_rate*100:.1f}%")

    # Persist JSON
    if args.output:
        outpath = args.output
        if outpath == "auto":
            outpath = f"/tmp/loadtest-results-{int(time.time())}.json"
        with open(outpath, "w") as f:
            json.dump(summary, f, indent=2)
        print(f"   Results saved to {outpath}")

    return summary


def main():
    parser = argparse.ArgumentParser(description="Enhanced loadtest runner v2")
    parser.add_argument("--clients", type=int, default=25)
    parser.add_argument("--rounds", type=int, default=50)
    parser.add_argument("--gateway", type=str, default="http://localhost:8082")
    parser.add_argument("--api-key", type=str, default=None)
    parser.add_argument("--api-keys", type=str, default=None, help="Comma-separated multiple keys")
    parser.add_argument("--model", type=str, default="loadtest-mini-alpha")
    parser.add_argument("--models", type=str, default=None, help="Comma-separated models")
    parser.add_argument("--sessions", type=int, default=0, help="Session pool size (0=per-client)")
    parser.add_argument("--session-reuse-ratio", type=float, default=0.0)
    parser.add_argument("--prompt-size", type=str, default="short",
                        choices=["short", "medium", "long", "mixed"])
    parser.add_argument("--stream", action="store_true")
    parser.add_argument("--max-history", type=int, default=10)
    parser.add_argument("--profile-config", type=str,
                        default=os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                             "profiles", "provider-profiles.json"))
    parser.add_argument("--output", type=str, default=None, help="JSON output path (or 'auto')")
    args = parser.parse_args()

    asyncio.run(run_test(args))


if __name__ == "__main__":
    main()
