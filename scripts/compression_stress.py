#!/usr/bin/env python3
"""Exercise gateway session compression with ordered, bounded conversations.

Each worker owns one session and submits its turns serially. Workers run in
parallel, so this preserves the session cache invariant while loading the
gateway. Compression strategy is intentionally not inferred from HTTP status:
audit request_logs.compression_strategy after the run using client request IDs.
"""

import argparse
import json
import os
import sys
import threading
import time
import uuid
from collections import Counter

import requests


def build_payload(turn_count):
    messages = [{"role": "system", "content": "You are a helpful assistant. " * 100}]
    for turn in range(turn_count):
        messages.append(
            {
                "role": "user",
                "content": f"Turn {turn}: " + "Lorem ipsum dolor sit amet, " * 300,
            }
        )
        if turn:
            messages.append(
                {
                    "role": "assistant",
                    "content": f"Answer {turn}: "
                    + "Analysis remains consistent. " * 80,
                }
            )
    return {
        "model": "gpt-4o",
        "messages": messages,
        "stream": False,
        "temperature": 0.0,
        "max_tokens": 16,
    }


class Stats:
    def __init__(self):
        self.lock = threading.Lock()
        self.statuses = Counter()
        self.latencies_ms = []
        self.degraded = 0
        self.errors = Counter()
        self.client_request_ids = []

    def record(self, status, latency_ms, degraded, request_id, error=None):
        with self.lock:
            self.statuses[status] += 1
            self.latencies_ms.append(latency_ms)
            self.degraded += int(degraded)
            if request_id:
                self.client_request_ids.append(request_id)
            if error:
                self.errors[error] += 1

    def percentile(self, value):
        if not self.latencies_ms:
            return 0.0
        ordered = sorted(self.latencies_ms)
        index = min(len(ordered) - 1, int(len(ordered) * value / 100))
        return ordered[index]

    def report(self, elapsed):
        total = sum(self.statuses.values())
        success = sum(
            count for status, count in self.statuses.items() if 200 <= status < 400
        )
        client_errors = sum(
            count for status, count in self.statuses.items() if 400 <= status < 500
        )
        server_errors = total - success - client_errors
        average = sum(self.latencies_ms) / max(1, len(self.latencies_ms))
        lines = [
            "=" * 64,
            " SESSION COMPRESSION TRANSPORT STRESS REPORT",
            "=" * 64,
            f"Requests:             {total}",
            f"Elapsed:              {elapsed:.1f}s",
            f"RPS:                  {total / max(elapsed, 0.001):.1f}",
            f"HTTP success (2xx/3xx): {success}",
            f"Client errors (4xx):   {client_errors}",
            f"Server/network errors: {server_errors}",
            f"Degraded header seen:  {self.degraded}",
            "Status distribution:",
        ]
        lines.extend(
            f"  HTTP {status}: {count}"
            for status, count in sorted(self.statuses.items())
        )
        if self.errors:
            lines.append("Transport errors:")
            lines.extend(
                f"  {name}: {count}" for name, count in sorted(self.errors.items())
            )
        lines.extend(
            [
                "Latency (ms):",
                f"  avg={average:.1f} p50={self.percentile(50):.1f}",
                f"  p95={self.percentile(95):.1f} p99={self.percentile(99):.1f}",
                "Compression is NOT proven by this HTTP report alone.",
                "Query request_logs by the printed client request IDs and require",
                "compression_strategy in {delta_append, sliding_window_*, mechanical_trim}.",
                "=" * 64,
            ]
        )
        return "\n".join(lines)


def worker(worker_index, turns, max_turns, url, api_key, stats, timeout):
    from requests.adapters import HTTPAdapter

    session = requests.Session()
    adapter = HTTPAdapter(pool_connections=1, pool_maxsize=1)
    session.mount("http://", adapter)
    session.mount("https://", adapter)
    session_id = "gw_" + str(uuid.uuid4())

    for sequence in range(turns):
        turn = sequence % max_turns + 1
        if turn == 1 and sequence:
            session_id = "gw_" + str(uuid.uuid4())
        client_request_id = "compression-stress-" + uuid.uuid4().hex
        headers = {
            "Authorization": "Bearer " + api_key,
            "Content-Type": "application/json",
            "X-Gw-Session-Id": session_id,
            "X-Request-Id": client_request_id,
        }
        started = time.monotonic()
        try:
            response = session.post(
                url,
                data=json.dumps(build_payload(turn)),
                headers=headers,
                timeout=timeout,
            )
            response.content
            stats.record(
                response.status_code,
                (time.monotonic() - started) * 1000,
                response.headers.get("X-Gw-Compression-Degraded") != "",
                client_request_id,
            )
        except requests.RequestException as error:
            stats.record(
                0,
                (time.monotonic() - started) * 1000,
                False,
                client_request_id,
                type(error).__name__,
            )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:8781/v1/chat/completions")
    parser.add_argument(
        "--api-key", default=os.getenv("LLM_GATEWAY_STRESS_API_KEY", "")
    )
    parser.add_argument(
        "--concurrency", type=int, default=20, help="independent, ordered sessions"
    )
    parser.add_argument("--turns-per-session", type=int, default=10)
    parser.add_argument(
        "--max-turns",
        type=int,
        default=10,
        help="reset each session after this many turns",
    )
    parser.add_argument("--timeout", type=float, default=30.0)
    parser.add_argument(
        "--request-ids-output", default="compression-stress-request-ids.txt"
    )
    args = parser.parse_args()

    if not args.api_key.startswith("sk-"):
        parser.error(
            "--api-key or LLM_GATEWAY_STRESS_API_KEY with an sk- prefix is required"
        )
    if args.concurrency < 1 or args.turns_per_session < 1 or args.max_turns < 1:
        parser.error("concurrency, turns-per-session, and max-turns must be positive")

    stats = Stats()
    started = time.monotonic()
    threads = [
        threading.Thread(
            target=worker,
            args=(
                index,
                args.turns_per_session,
                args.max_turns,
                args.url,
                args.api_key,
                stats,
                args.timeout,
            ),
        )
        for index in range(args.concurrency)
    ]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    elapsed = time.monotonic() - started

    with open(args.request_ids_output, "w") as output:
        output.write("\n".join(stats.client_request_ids) + "\n")
    print(stats.report(elapsed))
    print(f"Client request IDs written to {args.request_ids_output}")
    if any(status >= 500 or status == 0 for status in stats.statuses):
        sys.exit(1)


if __name__ == "__main__":
    main()
