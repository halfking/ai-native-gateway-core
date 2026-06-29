#!/usr/bin/env python3
"""
Tight-burst concurrent load generator for fp_slot verification.

Launches N requests as close to simultaneously as possible (using a barrier),
writes each response body + status to RESULTS_DIR/resp-N.json / status-N.txt.
Uses http.client directly so we tolerate the gateway's slightly-mismatched
Content-Length after StripMinimaxFieldsBody (some bytes off is harmless —
the response is JSON-decodable anyway).

Args (positional, all required):
  argv[1] = gateway port
  argv[2] = concurrency
  argv[3] = sticky session id (X-Session-Id)
  argv[4] = results directory
  argv[5] = upstream latency budget in ms (used as HTTP timeout margin)
"""
import sys
import json
import os
import threading
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from urllib.parse import urlparse
import http.client

PORT = int(sys.argv[1])
CONCURRENCY = int(sys.argv[2])
STICKY = sys.argv[3]
RESULTS_DIR = sys.argv[4]
MOCK_LATENCY_HINT_MS = int(sys.argv[5])

URL = f"http://127.0.0.1:{PORT}/v1/chat/completions"
BODY = json.dumps({
    "model": "minimax-m3",
    "messages": [{"role": "user", "content": "hello fp_slot verify"}],
    "stream": False,
}).encode("utf-8")

# API key: prefer env var so the script never carries plaintext creds on the
# command line. The env var is set by the parent verify-fpslot-fix.sh.
API_KEY = os.environ.get("FPSLOT_RUN_API_KEY", "fpverify-fallback")
HEADERS = {
    "Authorization": f"Bearer {API_KEY}",
    "Content-Type": "application/json",
    "X-Tenant-Id": "default",
    "X-Session-Id": STICKY,
    "X-Api-Key": API_KEY,
}

barrier = threading.Barrier(CONCURRENCY)


def fire(idx):
    barrier.wait()  # all threads release at the same instant
    started = time.monotonic()
    try:
        u = urlparse(URL)
        conn = http.client.HTTPConnection(
            u.hostname, u.port,
            timeout=MOCK_LATENCY_HINT_MS / 1000 + 30,
        )
        path = u.path or "/"
        if u.query:
            path = f"{path}?{u.query}"
        conn.request("POST", path, body=BODY, headers=HEADERS)
        resp = conn.getresponse()
        status = resp.status
        # Read all bytes, ignoring Content-Length drift.
        body = b""
        while True:
            chunk = resp.read(65536)
            if not chunk:
                break
            body += chunk
        conn.close()
    except Exception as e:
        status = 0
        body = f'{{"error":"{type(e).__name__}: {e}"}}'.encode("utf-8")
    elapsed_ms = (time.monotonic() - started) * 1000
    with open(f"{RESULTS_DIR}/resp-{idx}.json", "wb") as f:
        f.write(body)
    with open(f"{RESULTS_DIR}/status-{idx}.txt", "w") as f:
        f.write(str(status))
    with open(f"{RESULTS_DIR}/lat-{idx}.txt", "w") as f:
        f.write(f"{elapsed_ms:.0f}")
    return idx, status, elapsed_ms


if __name__ == "__main__":
    t0 = time.monotonic()
    with ThreadPoolExecutor(max_workers=CONCURRENCY) as ex:
        futs = [ex.submit(fire, i + 1) for i in range(CONCURRENCY)]
        results = [f.result() for f in as_completed(futs)]
    wall = (time.monotonic() - t0) * 1000
    succ = sum(1 for _, s, _ in results if s == 200)
    print(
        f"launched {CONCURRENCY} requests in parallel; "
        f"wall={wall:.0f}ms; succ={succ}/{CONCURRENCY}",
        file=sys.stderr,
    )