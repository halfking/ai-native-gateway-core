# tests/local/mock-client/client.py
#
# 单个 mock 客户端 — 持续以固定速率（泊松分布）打 gateway。
# 主要统计：success rate / p99 / 错误类型分布 / 上游耗时。
#
# Usage:
#   python3 client.py --id c1 --rps 2 --duration 60 --models minimax-m3,gpt-4o
#
# 或用 orchestrator 启动多 client：

import argparse
import asyncio
import contextlib
import json
import os
import random
import signal
import sys
import time
from collections import defaultdict

import aiohttp

# 让 client.py 既可 import 也可执行
ROOT = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, ROOT)
import scenarios  # noqa: E402


class ClientStats:
    def __init__(self, client_id: str):
        self.client_id = client_id
        self.started_at = time.time()
        self.total = 0
        self.succ = 0
        self.fail = 0
        self.latencies = []
        self.errors_by_kind = defaultdict(int)
        self.errors_by_status = defaultdict(int)
        self.sessions = defaultdict(list)  # 维护 session_id → last N messages
        self.lock = asyncio.Lock() if asyncio.get_event_loop().is_running() else None
        self._lock = None

    def _get_lock(self):
        if self._lock is None:
            self._lock = asyncio.Lock()
        return self._lock

    def snapshot(self) -> dict:
        lats = sorted(self.latencies)
        n = len(lats)
        p50 = lats[n // 2] if n else 0
        p99 = lats[int(n * 0.99)] if n else 0
        elapsed = time.time() - self.started_at
        return {
            "client": self.client_id,
            "elapsed_sec": elapsed,
            "total": self.total,
            "succ": self.succ,
            "fail": self.fail,
            "rps": self.total / elapsed if elapsed else 0,
            "success_rate": (self.succ / self.total) if self.total else 0,
            "p50_ms": p50 * 1000,
            "p99_ms": p99 * 1000,
            "errors_by_status": dict(self.errors_by_status),
            "errors_by_kind": dict(self.errors_by_kind),
        }


class MockClient:
    def __init__(
        self,
        client_id: str,
        gateway: str,
        api_key: str,
        target_rps: float,
        duration: int,
        model_filter=None,
    ):
        self.cid = client_id
        self.gateway = gateway.rstrip("/")
        self.api_key = api_key
        self.target_rps = target_rps
        self.duration = duration
        self.model_filter = model_filter
        self.stats = ClientStats(client_id)
        self.running = False
        self.session_id_counter = 0

    def new_session_id(self) -> str:
        self.session_id_counter += 1
        return f"sess-{self.cid}-{self.session_id_counter}"

    async def send_one(self, session: aiohttp.ClientSession, body: dict):
        url = f"{self.gateway}/v1/chat/completions"
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {self.api_key}",
        }
        if body.get("model") in ("minimax-m3", "chaos-mid_drop", "chaos-tool_bug"):
            # 让某些请求携带 session id 触发 session_cache
            sess_id = self.new_session_id()
            headers["X-Gw-Session-Id"] = sess_id

        t0 = time.monotonic()
        status = 0
        err_kind = "transport"
        body_text = ""
        try:
            async with session.post(
                url, headers=headers, json=body, timeout=aiohttp.ClientTimeout(total=30)
            ) as resp:
                status = resp.status
                body_text = await resp.text()
                # 当上游的 status 是 200 而 upstream 是 stream 时 body_text 只含部分内容
                if body.get("stream"):
                    async for line in resp.content:  # noqa
                        pass
        except asyncio.TimeoutError:
            err_kind = "timeout"
        except aiohttp.ClientError as e:
            err_kind = "client_error"
        except Exception as e:
            err_kind = f"unknown:{e.__class__.__name__}"

        dur = time.monotonic() - t0
        await self._record(status, dur, err_kind, body, body_text)

    async def _record(
        self, status: int, dur: float, err_kind: str, body: dict, resp_text: str
    ):
        async with self.stats._get_lock():
            self.stats.total += 1
            self.stats.latencies.append(dur)
            if status == 200:
                self.stats.succ += 1
            else:
                self.stats.fail += 1
                # try parse error kind from response body
                kind = err_kind
                try:
                    j = json.loads(resp_text or "{}")
                    if isinstance(j, dict):
                        err = j.get("error", {})
                        if isinstance(err, dict):
                            kind = err.get("type", err_kind)
                        # also try "error.kind" style field from gateway
                        kind = (
                            j.get("error", {}).get("type", kind)
                            if isinstance(j.get("error"), dict)
                            else kind
                        )
                        if "error.kind" in (resp_text or ""):
                            kind = j.get("error_kind", kind) or kind
                except Exception:
                    pass
                self.stats.errors_by_status[status] += 1
                self.stats.errors_by_kind[kind or err_kind] += 1

    async def run(self):
        self.running = True
        async with aiohttp.ClientSession() as session:
            end_at = time.monotonic() + self.duration
            interval = 1.0 / max(self.target_rps, 0.1)
            while self.running and time.monotonic() < end_at:
                body = scenarios.sample_turn(self.cid, self.stats.sessions)
                if self.model_filter and body.get("model") not in self.model_filter:
                    pass  # still call but we'll filter results
                task = asyncio.create_task(self.send_one(session, body))
                # collect task result without awaiting (async fire-and-forget within budget)
                await asyncio.sleep(interval)


async def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--id", default=f"c{random.randint(1000, 9999)}")
    parser.add_argument("--gateway", default="http://localhost:58781")
    parser.add_argument("--api-key", default="sk-loc-1234567890abcdef")
    parser.add_argument("--rps", type=float, default=2.0)
    parser.add_argument("--duration", type=int, default=60)
    parser.add_argument(
        "--models",
        default=None,
        help="comma-separated, e.g. minimax-m3,claude-sonnet-5",
    )
    parser.add_argument(
        "--print-each", type=int, default=20, help="每 N 次打印一次累计统计"
    )
    args = parser.parse_args()

    model_filter = set(args.models.split(",")) if args.models else None

    client = MockClient(
        args.id, args.gateway, args.api_key, args.rps, args.duration, model_filter
    )

    # 启动定期打印
    async def printer():
        for _ in range(args.duration * 4):
            await asyncio.sleep(max(0.25, args.print_each / (args.rps * 4)))
            snap = client.stats.snapshot()
            print(
                f"[{snap['client']}] t={snap['elapsed_sec']:6.1f}s "
                f"total={snap['total']:5d} succ={snap['succ']:5d} fail={snap['fail']:4d} "
                f"succ_rate={snap['success_rate'] * 100:5.1f}% "
                f"p50={snap['p50_ms']:5.0f}ms p99={snap['p99_ms']:5.0f}ms "
                f"errors={dict(list(snap['errors_by_status'].items())[:3])}",
                flush=True,
            )

    try:
        await asyncio.gather(client.run(), printer())
    except asyncio.CancelledError:
        client.running = False

    snap = client.stats.snapshot()
    print(json.dumps(snap, indent=2, ensure_ascii=False))


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(0)
