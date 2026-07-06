"""
Mock LLM upstream v2 for loadtest + routing verification.

Extends v1 with:
- 10 state modes (healthy, slow, rate_limited, server_error, etc.)
- /admin/state REST API for dynamic state mutation
- State persistence (state.json)
- Real-time counters + history

v1 Environment variables (legacy, overridden by state mode):
  MOCK_LATENCY_MS_MIN    min response latency in ms (default 200)
  MOCK_LATENCY_MS_MAX    max response latency in ms (default 500)
  MOCK_PORT              listen port (default 18080)
  MOCK_TOKEN             token printed on every reply (for log correlation)
  MOCK_FAILURE_RATE      (deprecated, use state mode instead)

Run: python3 scripts/mocks/llm-mock-upstream/server.py
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

# Legacy env vars (v2 defaults to faster latency for baseline)
LATENCY_MIN = int(os.environ.get("MOCK_LATENCY_MS_MIN", "200"))
LATENCY_MAX = int(os.environ.get("MOCK_LATENCY_MS_MAX", "500"))
PORT = int(os.environ.get("MOCK_PORT", "18080"))
TOKEN = os.environ.get("MOCK_TOKEN", f"mock-{PORT}")
STATE_FILE = Path(os.environ.get("MOCK_STATE_FILE", f"/tmp/mock-state-{PORT}.json"))


# Global state (mutable at runtime via /admin/state)
class MockState:
    def __init__(self):
        self.mode = "healthy"  # healthy / slow / rate_limited / quota_exceeded / auth_error / server_error / timeout / connection_refused / broken_stream / flaky
        self.since = datetime.now(timezone.utc).isoformat()
        self.ttl_seconds = 0  # 0 = permanent, >0 = auto-revert to healthy after TTL
        self.latency_min_ms = LATENCY_MIN
        self.latency_max_ms = LATENCY_MAX
        self.counters = {
            "requests_total": 0,
            "requests_success": 0,
            "requests_error": 0,
            "state_changes": 0,
        }
        self.history = []  # last 100 state transitions

    def to_dict(self):
        return {
            "mode": self.mode,
            "since": self.since,
            "ttl_seconds": self.ttl_seconds,
            "latency_min_ms": self.latency_min_ms,
            "latency_max_ms": self.latency_max_ms,
            "counters": self.counters.copy(),
            "history": self.history[-100:],  # last 100
        }

    def save(self):
        STATE_FILE.write_text(json.dumps(self.to_dict(), indent=2))

    def load(self):
        if STATE_FILE.exists():
            data = json.loads(STATE_FILE.read_text())
            self.mode = data.get("mode", "healthy")
            self.since = data.get("since", self.since)
            self.ttl_seconds = data.get("ttl_seconds", 0)
            self.latency_min_ms = data.get("latency_min_ms", LATENCY_MIN)
            self.latency_max_ms = data.get("latency_max_ms", LATENCY_MAX)
            self.counters = data.get("counters", self.counters)
            self.history = data.get("history", [])
            print(f"[{TOKEN}] loaded state: mode={self.mode}, since={self.since}")

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
    # NOTE: we omit `object` and other fields that the gateway's
    # StripMinimaxFieldsBody would strip anyway (object, request_id,
    # usage_extra, ...). This keeps the body length consistent with what
    # the gateway will pass through to the client. Without this, the
    # gateway emits Content-Length=N (mock's full size) but the actual
    # body is N-K (after stripping), causing client IncompleteRead.
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
    }


def make_chunk(model: str, request_id: str, content: str) -> str:
    chunk = {
        "id": f"chatcmpl-{request_id}",
        "object": "chat.completion.chunk",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "delta": {"content": content},
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
    except:
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": "invalid JSON", "type": "invalid_request_error"}},
            status=400,
        )

    model = body.get("model", "unknown")
    messages = body.get("messages", [])
    is_stream = body.get("stream", False)

    user_last = ""
    for m in messages:
        if m.get("role") == "user":
            user_last = m.get("content", "")
    if isinstance(user_last, list):
        user_last = json.dumps(user_last)

    # State machine: behavior depends on current mode
    mode = STATE.mode

    # Check TTL expiry (auto-revert to healthy)
    if STATE.ttl_seconds > 0:
        elapsed = (
            datetime.now(timezone.utc).timestamp()
            - datetime.fromisoformat(STATE.since).timestamp()
        )
        if elapsed >= STATE.ttl_seconds:
            STATE.change_mode("healthy", ttl_seconds=0)
            mode = "healthy"

    # Mode-specific behavior
    if mode == "healthy":
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)
        # success path (below)

    elif mode == "slow":
        delay_ms = random.randint(5000, 30000)  # 5-30s slow
        await asyncio.sleep(delay_ms / 1000.0)
        # success path

    elif mode == "rate_limited":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {
                "error": {
                    "message": "Rate limit exceeded",
                    "type": "rate_limit_exceeded",
                    "code": "rate_limit",
                }
            },
            status=429,
            headers={"Retry-After": "30"},
        )

    elif mode == "quota_exceeded":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {
                "error": {
                    "message": "Insufficient quota",
                    "type": "insufficient_quota",
                    "code": "insufficient_quota",
                }
            },
            status=429,
        )

    elif mode == "auth_error":
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {
                "error": {
                    "message": "Invalid API key",
                    "type": "invalid_api_key",
                    "code": "invalid_api_key",
                }
            },
            status=401,
        )

    elif mode == "server_error":
        # 50% chance of 500
        if random.random() < 0.5:
            STATE.counters["requests_error"] += 1
            elapsed = (time.monotonic() - started) * 1000
            print(
                f"[{TOKEN}] req={request_id} mode=server_error → 500 after {elapsed:.0f}ms",
                flush=True,
            )
            return web.json_response(
                {
                    "error": {
                        "message": "Internal server error",
                        "type": "server_error",
                        "code": "internal",
                    }
                },
                status=500,
            )
        # else success path
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)

    elif mode == "timeout":
        # Never respond (client will timeout)
        await asyncio.sleep(3600)  # 1h
        STATE.counters["requests_error"] += 1
        return web.Response(status=504)

    elif mode == "connection_refused":
        # Simulate TCP RST (close connection immediately)
        STATE.counters["requests_error"] += 1
        raise web.HTTPServiceUnavailable(text="Connection refused simulation")

    elif mode == "broken_stream":
        # Start stream then break mid-way
        if is_stream:
            response = web.StreamResponse(
                status=200, headers={"Content-Type": "text/event-stream"}
            )
            await response.prepare(request)
            # Send one chunk then close
            chunk = make_chunk(model, request_id, "broken")
            await response.write(chunk.encode())
            STATE.counters["requests_error"] += 1
            await response.write_eof()
            return response
        else:
            # Non-stream: just 500
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "broken", "type": "server_error"}}, status=500
            )

    elif mode == "flaky":
        # 30% error, 70% success
        if random.random() < 0.3:
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "flaky 500", "type": "server_error"}}, status=500
            )
        delay_ms = random.randint(50, 200)
        await asyncio.sleep(delay_ms / 1000.0)

    else:
        # Unknown mode, treat as healthy
        delay_ms = random.randint(STATE.latency_min_ms, STATE.latency_max_ms)
        await asyncio.sleep(delay_ms / 1000.0)

    # Success path: return normal response
    content = f"echo: {user_last[:80]}"

    if is_stream:
        response = web.StreamResponse(
            status=200,
            headers={
                "Content-Type": "text/event-stream",
                "Cache-Control": "no-cache",
                "X-Request-Id": request_id,
            },
        )
        await response.prepare(request)

        # Chunk the reply: 3 chunks spread across ~80% of the latency budget,
        # simulating token-by-token streaming.
        words = content.split()
        chunk_count = max(1, len(words))
        per_chunk_sleep = (delay_ms / 1000.0 * 0.8) / chunk_count
        for i, w in enumerate(words):
            await response.write(make_chunk(model, request_id, w + " ").encode("utf-8"))
            await asyncio.sleep(per_chunk_sleep)

        # Final chunk with finish_reason.
        final = {
            "id": f"chatcmpl-{request_id}",
            "object": "chat.completion.chunk",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }
        await response.write(f"data: {json.dumps(final)}\n\n".encode("utf-8"))
        await response.write(b"data: [DONE]\n\n")

        elapsed = (time.monotonic() - started) * 1000
        print(
            f"[{TOKEN}] req={request_id} STREAM done after {elapsed:.0f}ms "
            f"(budget={delay_ms}ms, model={model})",
            flush=True,
        )
        return response

    body_out = make_response(model, content, request_id)
    # NOTE: do NOT add custom top-level fields like x_request_id to the body.
    # The gateway's StripMinimaxFieldsBody hook strips non-standard fields
    # from the upstream response, which would cause Content-Length to
    # disagree with the actual body bytes (client gets IncompleteRead).
    # The X-Request-Id response HEADER is still set below for log correlation.
    elapsed = (time.monotonic() - started) * 1000
    print(
        f"[{TOKEN}] req={request_id} OK after {elapsed:.0f}ms "
        f"(budget={delay_ms}ms, model={model})",
        flush=True,
    )
    return web.json_response(body_out, headers={"X-Request-Id": request_id})


async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response(
        {
            "status": "ok",
            "token": TOKEN,
            "latency_ms_range": [LATENCY_MIN, LATENCY_MAX],
            "failure_rate": FAILURE_RATE,
        }
    )


async def handle_models(_request: web.Request) -> web.Response:
    """OpenAI-compatible /v1/models endpoint.

    The gateway's credential cycler probes this to verify the upstream
    is alive. Without it the probe gets 404 and marks the credential
    health_status='unreachable', which makes is_routable=false.
    """
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


def build_app() -> web.Application:
    app = web.Application()
    app.router.add_post("/v1/chat/completions", handle_chat)
    app.router.add_get("/v1/models", handle_models)
    app.router.add_get("/healthz", handle_health)
    app.router.add_get("/", handle_health)
    return app


if __name__ == "__main__":
    print(
        f"[{TOKEN}] starting mock upstream on :{PORT}, "
        f"latency={LATENCY_MIN}-{LATENCY_MAX}ms, "
        f"failure_rate={FAILURE_RATE}",
        flush=True,
    )
    web.run_app(build_app(), host="0.0.0.0", port=PORT, print=None)
