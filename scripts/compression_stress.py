#!/usr/bin/env python3
"""
compression_stress.py - 会话压缩算法高并发压力测试

在 154 服务器上对本地 gateway (127.0.0.1:8781) 发起高并发 HTTP 请求，
触发 SessionCompressor 算法的完整执行流程。

Usage:
    python3 compression_stress.py --total 10000 --concurrency 200 --turns 10
"""

import argparse
import sys
import time
import json
import uuid
import threading
import requests
from queue import Queue
from collections import defaultdict


# ──────────────────────────────────────────────────────────────────────────────
# Test data: 构建触发压缩的大型会话
# ──────────────────────────────────────────────────────────────────────────────


def build_payload(turn_count: int) -> dict:
    """构造一个 n 轮对话的 chat 请求,触发压缩触发器。"""
    msgs = [{"role": "system", "content": "You are a helpful assistant. " * 100}]

    for i in range(turn_count):
        user_content = (
            f"Turn {i}: "
            + ("Lorem ipsum dolor sit amet, " * 300)
            + f" Please analyze this text and provide a detailed response. " * 5
            + f"Discuss the implications and provide examples."
        )
        msgs.append({"role": "user", "content": user_content})

        if i > 0:
            msgs.append(
                {
                    "role": "assistant",
                    "content": (
                        f"Assistant response for turn {i}: "
                        + ("Based on the analysis, I recommend " * 50)
                        + f"considering multiple factors. " * 20
                    ),
                }
            )

    return {
        "model": "gpt-4o",
        "messages": msgs,
        "stream": False,
        "temperature": 0.7,
        "max_tokens": 100,
    }


# ──────────────────────────────────────────────────────────────────────────────
# Stats collection
# ──────────────────────────────────────────────────────────────────────────────


class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.total_sent = 0
        self.successful = 0
        self.failed = 0
        self.errors = defaultdict(int)
        self.latencies = []
        self.start_time = 0.0
        self.end_time = 0.0
        self.bytes_sent = 0
        self.bytes_received = 0

    def record(self, status: int, latency_ms: float, sent: int, received: int):
        with self.lock:
            self.total_sent += 1
            self.bytes_sent += sent
            self.bytes_received += received
            self.latencies.append(latency_ms)
            if 200 <= status < 500:
                self.successful += 1
            else:
                self.failed += 1
                self.errors[status] += 1

    def percentile(self, p):
        if not self.latencies:
            return 0
        s = sorted(self.latencies)
        idx = max(0, min(int(len(s) * p / 100), len(s) - 1))
        return s[idx]

    def report(self):
        if not self.start_time or not self.end_time:
            return ""
        duration = self.end_time - self.start_time
        rps = self.total_sent / duration if duration > 0 else 0

        lines = [
            "",
            "=" * 64,
            "     COMPRESSION BENCHMARK - STRESS TEST REPORT",
            "=" * 64,
            f"Total sent:        {self.total_sent}",
            f"Duration:          {duration:.1f}s",
            f"RPS:               {rps:.1f}",
            f"Successful (2xx-4xx): {self.successful} ({self.successful * 100 / max(1, self.total_sent):.1f}%)",
            f"Failed (5xx):      {self.failed}",
            f"Bytes sent:        {self.bytes_sent / 1024 / 1024:.1f} MB",
            f"Bytes received:    {self.bytes_received / 1024 / 1024:.1f} MB",
        ]
        if self.errors:
            lines.append("Status code distribution:")
            for code, cnt in sorted(self.errors.items()):
                lines.append(f"  HTTP {code}: {cnt}")
        lines.extend(
            [
                "",
                "Latency (ms):",
                f"  Min:     {min(self.latencies) if self.latencies else 0:.1f}",
                f"  Avg:     {sum(self.latencies) / max(1, len(self.latencies)):.1f}",
                f"  P50:     {self.percentile(50):.1f}",
                f"  P90:     {self.percentile(90):.1f}",
                f"  P95:     {self.percentile(95):.1f}",
                f"  P99:     {self.percentile(99):.1f}",
                f"  Max:     {max(self.latencies) if self.latencies else 0:.1f}",
                "=" * 64,
            ]
        )
        return "\n".join(lines)


# ──────────────────────────────────────────────────────────────────────────────
# Worker: send a single request
# ──────────────────────────────────────────────────────────────────────────────


def worker(url, queue, stats, session_pool, session_pool_size, turns, timeout_s=30):
    from requests.adapters import HTTPAdapter  # local import for type-checker

    session = requests.Session()
    adapter = HTTPAdapter(pool_connections=1, pool_maxsize=1)
    session.mount("http://", adapter)
    session.mount("https://", adapter)
    while True:
        idx = queue.get()
        if idx is None:
            queue.task_done()
            break

        sid = session_pool[idx % session_pool_size]
        payload = build_payload(turns)
        body_bytes = json.dumps(payload).encode("utf-8")

        headers = {
            "Content-Type": "application/json",
            "X-Gw-Session-Id": sid,
            "Authorization": "Bearer bench-test-token",
        }

        t0 = time.monotonic()
        try:
            r = session.post(url, data=body_bytes, headers=headers, timeout=timeout_s)
            latency = (time.monotonic() - t0) * 1000
            stats.record(r.status_code, latency, len(body_bytes), len(r.content))
        except requests.exceptions.Timeout:
            stats.record(599, timeout_s * 1000, len(body_bytes), 0)
        except Exception as e:
            latency = (time.monotonic() - t0) * 1000
            stats.record(0, latency, len(body_bytes), 0)
        finally:
            queue.task_done()


# ──────────────────────────────────────────────────────────────────────────────
# Main runner
# ──────────────────────────────────────────────────────────────────────────────


def run_stress(url, total, concurrency, turns, session_pool_size):
    print(f"[+] Pre-generating {session_pool_size} session IDs...")
    session_pool = [f"bench_{uuid.uuid4().hex[:32]}" for _ in range(session_pool_size)]

    print(f"[+] Starting {concurrency} worker threads...")
    q = Queue()
    for i in range(total):
        q.put(i)
    # add sentinel for each worker
    for _ in range(concurrency):
        q.put(None)

    stats = Stats()
    stats.start_time = time.monotonic()

    threads = []
    for _ in range(concurrency):
        t = threading.Thread(
            target=worker,
            args=(url, q, stats, session_pool, session_pool_size, turns),
            daemon=True,
        )
        t.start()
        threads.append(t)

    # progress reporter
    last_reported = 0
    while not q.empty() or any(t.is_alive() for t in threads):
        time.sleep(1)
        with stats.lock:
            sent = stats.total_sent
        if sent > last_reported and sent % 200 == 0:
            elapsed = time.monotonic() - stats.start_time
            rps = sent / max(elapsed, 0.01)
            print(
                f"  [{sent:>6}/{total}] elapsed={elapsed:.1f}s RPS={rps:.1f}",
                flush=True,
            )
            last_reported = sent
        if sent >= total:
            break

    for t in threads:
        t.join(timeout=5)

    stats.end_time = time.monotonic()
    return stats


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--url", default="http://127.0.0.1:8781", help="Gateway base URL"
    )
    parser.add_argument(
        "--total", type=int, default=10000, help="Total requests to send"
    )
    parser.add_argument(
        "--concurrency", type=int, default=200, help="Concurrent workers"
    )
    parser.add_argument(
        "--turns", type=int, default=10, help="Conversation turns per request"
    )
    parser.add_argument(
        "--session-pool",
        type=int,
        default=100,
        help="Unique session_ids for delta-append",
    )
    parser.add_argument(
        "--timeout", type=int, default=30, help="Request timeout seconds"
    )
    args = parser.parse_args()

    print(f"[+] compression_stress starting")
    print(f"    Target:         {args.url}")
    print(f"    Total requests: {args.total}")
    print(f"    Concurrency:    {args.concurrency}")
    print(f"    Turns/request:  {args.turns}")
    print(f"    Session pool:   {args.session_pool}")
    print()

    try:
        stats = run_stress(
            args.url,
            args.total,
            args.concurrency,
            args.turns,
            args.session_pool,
        )
        print(stats.report())
    except KeyboardInterrupt:
        print("\n[!] Interrupted")
        sys.exit(1)


if __name__ == "__main__":
    main()
