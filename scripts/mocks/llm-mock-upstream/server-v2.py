#!/usr/bin/env python3
"""
Mock LLM upstream v2 for loadtest + routing verification.

v2 Features:
- 10 state modes (healthy, slow, rate_limited, server_error, etc.)
- /admin/state REST API for dynamic state mutation
- State persistence (state.json)
- Real-time counters + history

Run: python3 server-v2.py
Env: MOCK_PORT=18080 MOCK_TOKEN=mock-A python3 server-v2.py
"""

import asyncio
import json
import os
import random
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from aiohttp import web

PORT = int(os.environ.get("MOCK_PORT", "18080"))
TOKEN = os.environ.get("MOCK_TOKEN", f"mock-{PORT}")
STATE_FILE = Path(os.environ.get("MOCK_STATE_FILE", f"/tmp/mock-state-{PORT}.json"))


class MockState:
    def __init__(self):
        self.mode = "healthy"
        self.since = datetime.now(timezone.utc).isoformat()
        self.ttl_seconds = 0
        self.latency_min_ms = 200
        self.latency_max_ms = 500
        self.counters = {
            "requests_total": 0,
            "requests_success": 0,
            "requests_error": 0,
            "state_changes": 0,
        }
        self.history = []

    def to_dict(self):
        return {
            "mode": self.mode,
            "since": self.since,
            "ttl_seconds": self.ttl_seconds,
            "latency_min_ms": self.latency_min_ms,
            "latency_max_ms": self.latency_max_ms,
            "counters": self.counters.copy(),
            "history": self.history[-100:],
        }

    def save(self):
        STATE_FILE.write_text(json.dumps(self.to_dict(), indent=2))

    def load(self):
        if STATE_FILE.exists():
            data = json.loads(STATE_FILE.read_text())
            self.mode = data.get("mode", "healthy")
            self.since = data.get("since", self.since)
            self.ttl_seconds = data.get("ttl_seconds", 0)
            self.latency_min_ms = data.get("latency_min_ms", 200)
            self.latency_max_ms = data.get("latency_max_ms", 500)
            self.counters = data.get("counters", self.counters)
            self.history = data.get("history", [])
            print(f"[{TOKEN}] loaded state: mode={self.mode}")

    def change_mode(self, new_mode, ttl_seconds=0, latency_min=None, latency_max=None):
        old_mode = self.mode
        self.mode = new_mode
        self.since = datetime.now(timezone.utc).isoformat()
        self.ttl_seconds = ttl_seconds
        if latency_min is not None:
            self.latency_min_ms = latency_min
        if latency_max is not None:
            self.latency_max_ms = latency_max
        self.counters["state_changes"] += 1
        self.history.append(
            {
                "from": old_mode,
                "to": new_mode,
                "at": self.since,
                "ttl": ttl_seconds,
            }
        )
        self.save()
        print(f"[{TOKEN}] state: {old_mode} → {new_mode} (ttl={ttl_seconds}s)")


STATE = MockState()
STATE.load()


def make_response(model: str, content: str, request_id: str) -> dict:
    return {
        "id": f"chatcmpl-{request_id}",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop",
            }
        ],
        "usage": {
            "prompt_tokens": 10,
            "completion_tokens": len(content.split()),
            "total_tokens": 10 + len(content.split()),
        },
        # 身份标识：客户端据此判断请求命中了哪个 mock 实例。
        # 同时放在顶层字段和 content 文本里，兼容两种提取方式。
        "_mock_identity": TOKEN,
    }


def make_chunk(model: str, request_id: str, content: str) -> str:
    chunk = {
        "id": f"chatcmpl-{request_id}",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "delta": {"role": "assistant", "content": content},
                "finish_reason": None,
            }
        ],
    }
    return f"data: {json.dumps(chunk)}\n\n"


async def handle_chat(request: web.Request) -> web.StreamResponse:
    started = time.monotonic()
    request_id = uuid.uuid4().hex[:16]
    STATE.counters["requests_total"] += 1

    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": f"invalid JSON: {e}"}}, status=400
        )

    model = body.get("model", "gpt-4o")
    messages = body.get("messages", [])
    is_stream = body.get("stream", False)

    user_last = ""
    for m in messages:
        if m.get("role") == "user":
            user_last = m.get("content", "")
    if isinstance(user_last, list):
        user_last = json.dumps(user_last)

    mode = STATE.mode

    # TTL check
    if STATE.ttl_seconds > 0:
        elapsed_sec = time.time() - datetime.fromisoformat(STATE.since).timestamp()
        if elapsed_sec >= STATE.ttl_seconds:
            STATE.change_mode("healthy", ttl_seconds=0)
            mode = "healthy"

    # State-specific behavior
    if mode == "healthy":
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)

    elif mode == "slow":
        delay_ms = random.randint(5000, 30000)
        await asyncio.sleep(delay_ms / 1000.0)

    elif mode == "rate_limited":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": "Rate limit", "type": "rate_limit_exceeded"}},
            status=429,
            headers={"Retry-After": "30"},
        )

    elif mode == "quota_exceeded":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": "Insufficient quota", "type": "insufficient_quota"}},
            status=429,
        )

    elif mode == "auth_error":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": "Invalid API key", "type": "invalid_api_key"}},
            status=401,
        )

    elif mode == "server_error":
        if random.random() < 0.5:
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Internal error", "type": "server_error"}},
                status=500,
            )
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)

    elif mode == "timeout":
        # Hold connection indefinitely — client will timeout on its side.
        # Never respond, never increment counter (client sees timeout, not 504).
        await asyncio.sleep(86400)  # 24h, effectively "forever"
        return web.Response(status=200)  # unreachable

    elif mode == "connection_refused":
        STATE.counters["requests_error"] += 1
        raise web.HTTPServiceUnavailable(text="Connection refused")

    elif mode == "broken_stream":
        if is_stream:
            response = web.StreamResponse(
                status=200, headers={"Content-Type": "text/event-stream"}
            )
            await response.prepare(request)
            # Send one partial chunk then abruptly abort (simulates TCP RST mid-stream)
            chunk = make_chunk(model, request_id, "broken")
            await response.write(chunk.encode())
            STATE.counters["requests_error"] += 1
            # Force-abort: close transport to simulate network failure
            if request.transport is not None:
                request.transport.close()
            return response
        else:
            STATE.counters["requests_error"] += 1
            return web.json_response({"error": {"message": "broken"}}, status=500)

    elif mode == "flaky":
        if random.random() < 0.3:
            STATE.counters["requests_error"] += 1
            return web.json_response({"error": {"message": "flaky"}}, status=500)
        delay_ms = random.randint(50, 200)
        await asyncio.sleep(delay_ms / 1000.0)

    else:
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)

    content = f"echo: {user_last[:80]} [mock={TOKEN}]"

    if is_stream:
        response = web.StreamResponse(
            status=200,
            headers={"Content-Type": "text/event-stream", "X-Request-Id": request_id},
        )
        await response.prepare(request)
        words = content.split()
        for word in words:
            chunk = make_chunk(model, request_id, word + " ")
            await response.write(chunk.encode())
            await asyncio.sleep(0.05)
        await response.write(b"data: [DONE]\n\n")
        STATE.counters["requests_success"] += 1
        elapsed = (time.monotonic() - started) * 1000
        print(
            f"[{TOKEN}] req={request_id} STREAM OK mode={mode} {elapsed:.0f}ms",
            flush=True,
        )
        return response

    STATE.counters["requests_success"] += 1
    elapsed = (time.monotonic() - started) * 1000
    print(f"[{TOKEN}] req={request_id} OK mode={mode} {elapsed:.0f}ms", flush=True)
    return web.json_response(make_response(model, content, request_id))


async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response(
        {"status": "ok", "token": TOKEN, "mode": STATE.mode, "since": STATE.since}
    )


async def handle_models(_request: web.Request) -> web.Response:
    return web.json_response(
        {
            "object": "list",
            "data": [
                {
                    "id": "gpt-4o",
                    "object": "model",
                    "created": int(time.time()),
                    "owned_by": "local-mock",
                },
                {
                    "id": "gpt-4o-mini",
                    "object": "model",
                    "created": int(time.time()),
                    "owned_by": "local-mock",
                },
            ],
        }
    )


async def handle_admin_state_get(_request: web.Request) -> web.Response:
    return web.json_response(STATE.to_dict())


async def handle_admin_state_set(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        return web.json_response({"error": f"invalid JSON: {e}"}, status=400)

    new_mode = body.get("mode")
    ttl_seconds = body.get("ttl_seconds", 0)
    latency_min = body.get("latency_min_ms")
    latency_max = body.get("latency_max_ms")

    valid_modes = [
        "healthy",
        "slow",
        "rate_limited",
        "quota_exceeded",
        "auth_error",
        "server_error",
        "timeout",
        "connection_refused",
        "broken_stream",
        "flaky",
    ]

    if new_mode not in valid_modes:
        return web.json_response(
            {"error": f"invalid mode, must be one of {valid_modes}"}, status=400
        )

    STATE.change_mode(new_mode, ttl_seconds, latency_min, latency_max)
    return web.json_response({"status": "ok", "new_state": STATE.to_dict()})


async def handle_admin_reset(_request: web.Request) -> web.Response:
    STATE.change_mode("healthy", ttl_seconds=0)
    STATE.counters = {
        "requests_total": 0,
        "requests_success": 0,
        "requests_error": 0,
        "state_changes": 1,
    }
    STATE.save()
    return web.json_response({"status": "ok", "message": "reset"})


async def handle_admin_metrics(_request: web.Request) -> web.Response:
    return web.json_response(
        {"token": TOKEN, "counters": STATE.counters, "mode": STATE.mode}
    )


async def handle_admin_history(_request: web.Request) -> web.Response:
    return web.json_response({"token": TOKEN, "history": STATE.history[-100:]})


async def handle_admin_latency(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        return web.json_response({"error": f"invalid JSON: {e}"}, status=400)

    if "min_ms" in body:
        STATE.latency_min_ms = int(body["min_ms"])
    if "max_ms" in body:
        STATE.latency_max_ms = int(body["max_ms"])
    STATE.save()
    return web.json_response(
        {
            "status": "ok",
            "latency_min_ms": STATE.latency_min_ms,
            "latency_max_ms": STATE.latency_max_ms,
        }
    )


def build_app() -> web.Application:
    app = web.Application()
    app.router.add_post("/v1/chat/completions", handle_chat)
    app.router.add_get("/v1/models", handle_models)
    app.router.add_get("/healthz", handle_health)
    app.router.add_get("/", handle_health)
    app.router.add_get("/admin/state", handle_admin_state_get)
    app.router.add_post("/admin/state", handle_admin_state_set)
    app.router.add_post("/admin/reset", handle_admin_reset)
    app.router.add_get("/admin/metrics", handle_admin_metrics)
    app.router.add_get("/admin/history", handle_admin_history)
    app.router.add_post("/admin/latency", handle_admin_latency)
    return app


if __name__ == "__main__":
    print(f"[{TOKEN}] starting mock v2 on :{PORT}, mode={STATE.mode}")
    web.run_app(build_app(), host="0.0.0.0", port=PORT)
