# docs/全方面测试/tools/loadtest.py
#
# 通用 loadtest 客户端：
#   - N 个并发客户端（每个 = 一个 session 里跑 RPS-per-client 速率）
#   - 多 API key 轮换
#   - sticky session（x-gw-session-id header）
#   - 单 model 多 model 列表
#   - 流式 / 非流式混合
#   - fault injection （在客户端做：网络异常 / cancel / 大消息）
#   - 输出 JSON 摘要（success / p50 / p95 / p99 / per-model）
#
# 用法：
#   python3 loadtest.py \
#     --gateway http://localhost:8781 \
#     --api-keys "sk-loadtest-01,sk-loadtest-02,..." \
#     --n-clients 80 --rps-per-client 8 --duration 60 \
#     --models loadtest-mini-alpha,loadtest-mini-beta,loadtest-standard-alpha \
#     --prompt short \
#     --output /tmp/S1.json

import argparse
import asyncio
import json
import os
import random
import sys
import time
from collections import defaultdict

import aiohttp

PROMPTS = {
    "short": "hi",
    "medium": "Please write a short story about a curious cat and a wise old owl going on an adventure together.",
    "long": open("/dev/null").read()
    if False
    else " ".join(
        [
            "Lorem ipsum dolor sit amet, consectetur adipiscing elit. "
            "Sed do eiusmod tempor incididunt ut labore et dolore magna aliqua. "
        ]
        * 200
    ),
}

MODELS_GROUP = {
    "core": ["loadtest-mini-alpha", "loadtest-mini-beta", "loadtest-standard-alpha"],
    "tier": [
        "loadtest-mini-alpha",
        "loadtest-mini-beta",
        "loadtest-mini-gamma",
        "loadtest-standard-alpha",
        "loadtest-standard-beta",
        "loadtest-standard-gamma",
        "loadtest-pro-alpha",
        "loadtest-pro-beta",
        "loadtest-pro-gamma",
    ],
    "all": [
        "loadtest-mini-alpha",
        "loadtest-mini-beta",
        "loadtest-mini-gamma",
        "loadtest-standard-alpha",
        "loadtest-standard-beta",
        "loadtest-standard-gamma",
        "loadtest-pro-alpha",
        "loadtest-pro-beta",
        "loadtest-pro-gamma",
        "loadtest-ultra-alpha",
        "loadtest-ultra-beta",
        "loadtest-ultra-gamma",
        "loadtest-vision-alpha",
        "loadtest-vision-beta",
        "loadtest-vision-gamma",
    ],
    "tok3": ["loadtest-mini-alpha", "loadtest-standard-alpha", "loadtest-pro-alpha"],
}


class Stats:
    def __init__(self):
        self.lock = asyncio.Lock()
        self.total = 0
        self.succ = 0
        self.fail_by_status = defaultdict(int)
        self.fail_by_kind = defaultdict(int)
        self.latencies_ms = []

    async def record(self, status: int, kind: str, dur_ms: float):
        async with self.lock:
            self.total += 1
            if 200 <= status < 300:
                self.succ += 1
            else:
                self.fail_by_status[status] += 1
                self.fail_by_kind[kind or "unknown"] += 1
            self.latencies_ms.append(dur_ms)

    def snapshot(self, started: float) -> dict:
        d = sorted(self.latencies_ms)
        n = len(d)

        def pct(p):
            return d[int(n * p)] if n else 0

        elapsed = time.monotonic() - started
        return {
            "total": self.total,
            "succ": self.succ,
            "fail": self.total - self.succ,
            "success_rate": (self.succ / self.total) if self.total else 0,
            "p50_ms": pct(0.50),
            "p95_ms": pct(0.95),
            "p99_ms": pct(0.99),
            "max_ms": d[-1] if d else 0,
            "throughput_rps": self.total / elapsed if elapsed else 0,
            "elapsed_sec": elapsed,
            "fail_by_status": dict(self.fail_by_status),
            "fail_by_kind": dict(self.fail_by_kind),
        }


async def one_request(
    session: aiohttp.ClientSession,
    gateway: str,
    api_key: str,
    model: str,
    prompt: str,
    session_id: str = None,
    stream: bool = False,
    timeout: int = 30,
):
    url = f"{gateway}/v1/chat/completions"
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {api_key}",
    }
    if session_id:
        headers["X-Gw-Session-Id"] = session_id
    body = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "max_tokens": random.choice([10, 30, 60]),
        "stream": stream,
    }
    t0 = time.monotonic()
    status = 0
    kind = "transport"
    try:
        async with session.post(
            url,
            headers=headers,
            json=body,
            timeout=aiohttp.ClientTimeout(total=timeout),
        ) as resp:
            status = resp.status
            text = await resp.text()
            if stream:
                # drain stream
                async for _ in resp.content:
                    pass
            # try parse kind from response body
            try:
                j = json.loads(text or "{}")
                err = j.get("error", {})
                if isinstance(err, dict):
                    kind = err.get("type") or err.get("code") or kind
            except Exception:
                pass
    except asyncio.TimeoutError:
        status = 408
        kind = "timeout"
    except aiohttp.ClientError as e:
        kind = f"client_error:{e.__class__.__name__}"
    except Exception as e:
        kind = f"exception:{e.__class__.__name__}"
    dur_ms = (time.monotonic() - t0) * 1000
    return status, kind, dur_ms


async def client_worker(
    client_id: int,
    gateway: str,
    api_keys: list,
    models: list,
    rps: float,
    duration: int,
    sticky_ratio: float,
    stats: Stats,
    sticky_pool_size: int = 20,
    stream_ratio: float = 0,
    prompt_size: str = "short",
    fault_inject_cancel: float = 0,
):
    """一个客户端 worker：以 rps 速率发请求。"""
    end_at = time.monotonic() + duration
    interval = 1.0 / max(rps, 0.1)
    prompt = PROMPTS.get(prompt_size, PROMPTS["short"])
    session_ids = [f"sess-{client_id}-{i}" for i in range(sticky_pool_size)]

    async with aiohttp.ClientSession() as session:
        while time.monotonic() < end_at:
            # choose api key (round-robin per client)
            ak = api_keys[client_id % len(api_keys)]
            # choose model
            m = random.choice(models)
            # choose session
            sid = None
            if random.random() < sticky_ratio:
                sid = random.choice(session_ids)
            # stream or not
            stream = random.random() < stream_ratio
            # fault injection: cancel randomly (close context mid-flight)
            if fault_inject_cancel > 0 and random.random() < fault_inject_cancel:
                t0 = time.monotonic()
                # fire-and-forget; cancel after 100-300ms
                url = f"{gateway}/v1/chat/completions"
                headers = {
                    "Content-Type": "application/json",
                    "Authorization": f"Bearer {ak}",
                }
                if sid:
                    headers["X-Gw-Session-Id"] = sid
                body = {
                    "model": m,
                    "messages": [{"role": "user", "content": prompt}],
                    "max_tokens": 30,
                }

                async def fire():
                    async with session.post(
                        url,
                        headers=headers,
                        json=body,
                        timeout=aiohttp.ClientTimeout(total=10),
                    ) as r:
                        await r.read()

                tk = asyncio.create_task(fire())
                await asyncio.sleep(random.uniform(0.05, 0.25))
                if not tk.done():
                    tk.cancel()
                try:
                    await tk
                except (asyncio.CancelledError, Exception):
                    pass
                dur_ms = (time.monotonic() - t0) * 1000
                # treat cancel as best-effort (status=499)
                await stats.record(499, "client_cancel", dur_ms)
                await asyncio.sleep(interval)
                continue

            status, kind, dur_ms = await one_request(
                session, gateway, ak, m, prompt, session_id=sid, stream=stream
            )
            await stats.record(status, kind, dur_ms)
            await asyncio.sleep(interval)


async def run(args):
    started = time.monotonic()  # 必须用 monotonic 与 client_worker 内部一致
    api_keys = [k.strip() for k in args.api_keys.split(",") if k.strip()]
    if args.models in MODELS_GROUP:
        models = MODELS_GROUP[args.models]
    else:
        models = [m.strip() for m in args.models.split(",") if m.strip()]

    # 把 api key pool 分配到 n-clients — round-robin
    stats = Stats()
    tasks = []
    for i in range(args.n_clients):
        t = asyncio.create_task(
            client_worker(
                i,
                args.gateway,
                api_keys,
                models,
                args.rps_per_client,
                args.duration,
                args.sticky_ratio,
                stats,
                sticky_pool_size=args.sticky_pool,
                stream_ratio=args.stream_ratio,
                prompt_size=args.prompt,
                fault_inject_cancel=args.fault_inject_cancel,
            )
        )
        tasks.append(t)

    # periodic reporter
    async def printer():
        if args.duration:
            end_realtime = started + args.duration + 3.0  # 多给 3s 让 client 收尾
        else:
            end_realtime = float("inf")
        while time.monotonic() < end_realtime:
            await asyncio.sleep(3.0)
            if time.monotonic() >= end_realtime:
                break
            snap = stats.snapshot(started)
            print(
                f"[t={snap['elapsed_sec']:6.1f}s] total={snap['total']:5d} "
                f"succ={snap['succ']:5d} fail={snap['fail']:5d} "
                f"rate={(snap['success_rate'] * 100):5.1f}% "
                f"p50={snap['p50_ms']:5.0f}ms p95={snap['p95_ms']:5.0f}ms "
                f"status={dict(snap['fail_by_status'])}",
                flush=True,
            )

    tasks.append(asyncio.create_task(printer()))

    try:
        await asyncio.gather(*tasks)
    except asyncio.CancelledError:
        pass
    final = stats.snapshot(started)
    out = {
        "scenario": args.scenario,
        "gateway": args.gateway,
        "models": models,
        "n_clients": args.n_clients,
        "rps_per_client": args.rps_per_client,
        "duration_sec": args.duration,
        "prompt_size": args.prompt,
        "sticky_ratio": args.sticky_ratio,
        "stream_ratio": args.stream_ratio,
        "fault_inject_cancel": args.fault_inject_cancel,
        "metrics": final,
    }
    print(json.dumps(out, indent=2, ensure_ascii=False))
    if args.output:
        with open(args.output, "w") as f:
            json.dump(out, f, indent=2, ensure_ascii=False)
        print(f"\n  → wrote {args.output}", flush=True)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--gateway", default="http://localhost:8781")
    parser.add_argument(
        "--api-keys",
        required=True,
        help="comma-separated. e.g. sk-loadtest-01,sk-loadtest-02",
    )
    parser.add_argument("--n-clients", type=int, default=80)
    parser.add_argument("--rps-per-client", type=float, default=8.0)
    parser.add_argument("--duration", type=int, default=60)
    parser.add_argument(
        "--models", default="core", help="comma-separated OR [core|tier|all|tok3]"
    )
    parser.add_argument(
        "--prompt", choices=["short", "medium", "long"], default="short"
    )
    parser.add_argument(
        "--stream-ratio", type=float, default=0.0, help="0..1 比例的请求走 stream=true"
    )
    parser.add_argument(
        "--sticky-ratio",
        type=float,
        default=0.0,
        help="0..1 比例的请求带 X-Gw-Session-Id",
    )
    parser.add_argument(
        "--sticky-pool", type=int, default=20, help="每个 client 的 session 池大小"
    )
    parser.add_argument(
        "--fault-inject-cancel",
        type=float,
        default=0.0,
        help="0..1 客户端随机取消（模拟网络异常）",
    )
    parser.add_argument("--output", help="JSON 输出路径")
    parser.add_argument("--scenario", default="custom")
    args = parser.parse_args()
    try:
        asyncio.run(run(args))
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    main()
