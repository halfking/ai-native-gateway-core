#!/usr/bin/env python3
"""Local mock pair for AL smoke: fixed-latency upstream + injectable-overhead gateway.

Callers: run_smoke.sh. Listens only on 127.0.0.1. No production I/O.
Synthetic response only. User instruction: scripts/bench/latency only; no prod loadtest.
"""

from __future__ import annotations

import argparse
import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def _json_bytes(obj: dict) -> bytes:
    return json.dumps(obj, ensure_ascii=False).encode("utf-8")


def make_upstream_handler(upstream_ms: float):
    class UpstreamHandler(BaseHTTPRequestHandler):
        def log_message(self, fmt: str, *args) -> None:
            return

        def do_GET(self) -> None:
            if self.path.startswith("/healthz"):
                self._ok({"ok": True, "role": "upstream"})
                return
            self.send_error(404)

        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            _ = self.rfile.read(length)
            time.sleep(max(0.0, upstream_ms) / 1000.0)
            body = {
                "id": "chatcmpl-mock",
                "object": "chat.completion",
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": "pong"},
                        "finish_reason": "stop",
                    }
                ],
                "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
            }
            self._ok(body)

        def _ok(self, obj: dict) -> None:
            raw = _json_bytes(obj)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    return UpstreamHandler


def make_gateway_handler(upstream_url: str, overhead_ms: float):
    import urllib.request

    class GatewayHandler(BaseHTTPRequestHandler):
        def log_message(self, fmt: str, *args) -> None:
            return

        def do_GET(self) -> None:
            if self.path.startswith("/healthz"):
                self._ok({"ok": True, "role": "fake-gateway", "overhead_ms": overhead_ms})
                return
            if self.path.startswith("/v1/models"):
                time.sleep(max(0.0, overhead_ms) / 1000.0)
                self._ok({"object": "list", "data": [{"id": "mock-model", "object": "model"}]})
                return
            self.send_error(404)

        def do_POST(self) -> None:
            length = int(self.headers.get("Content-Length", "0"))
            payload = self.rfile.read(length)
            time.sleep(max(0.0, overhead_ms) / 1000.0)
            req = urllib.request.Request(
                upstream_url.rstrip("/") + "/v1/chat/completions",
                data=payload,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def _ok(self, obj: dict) -> None:
            raw = _json_bytes(obj)
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

    return GatewayHandler


def main() -> int:
    ap = argparse.ArgumentParser(description="Local upstream+fake-gateway pair for latency smoke")
    ap.add_argument("--upstream-port", type=int, default=19090)
    ap.add_argument("--gateway-port", type=int, default=18090)
    ap.add_argument("--upstream-ms", type=float, default=40.0)
    ap.add_argument("--overhead-ms", type=float, default=12.0)
    ap.add_argument("--ready-file", default="")
    args = ap.parse_args()

    upstream = ThreadingHTTPServer(
        ("127.0.0.1", args.upstream_port), make_upstream_handler(args.upstream_ms)
    )
    gw_url = f"http://127.0.0.1:{args.upstream_port}"
    gateway = ThreadingHTTPServer(
        ("127.0.0.1", args.gateway_port),
        make_gateway_handler(gw_url, args.overhead_ms),
    )

    for srv in (upstream, gateway):
        threading.Thread(target=srv.serve_forever, daemon=True).start()

    ready = {
        "upstream_base": f"http://127.0.0.1:{args.upstream_port}",
        "gateway_base": f"http://127.0.0.1:{args.gateway_port}",
        "upstream_ms": args.upstream_ms,
        "overhead_ms": args.overhead_ms,
    }
    if args.ready_file:
        with open(args.ready_file, "w", encoding="utf-8") as f:
            json.dump(ready, f)
            f.write("\n")
    print(json.dumps({"event": "ready", **ready}), flush=True)

    try:
        while True:
            time.sleep(3600)
    except KeyboardInterrupt:
        pass
    finally:
        upstream.shutdown()
        gateway.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
