"""
Mock LLM upstream for fp_slot regression verification.

Mimics the OpenAI `/v1/chat/completions` endpoint (both stream and non-stream)
with a configurable response delay. The delay lets us reproduce the
production scenario where the upstream holds the connection open for several
seconds, so concurrent requests from the same sticky session pile up in the
gateway's fp_slot layer.

Environment variables:
  MOCK_LATENCY_MS_MIN    min response latency in ms (default 10000)
  MOCK_LATENCY_MS_MAX    max response latency in ms (default 15000)
  MOCK_PORT              listen port (default 18080)
  MOCK_TOKEN             token printed on every reply (for log correlation)
  MOCK_FAILURE_RATE      fraction of requests to fail (0.0 - 1.0, default 0.0)

Run: python3 scripts/mocks/llm-mock-upstream/server.py
"""

import asyncio
import json
import os
import random
import time
import uuid
from aiohttp import web

LATENCY_MIN = int(os.environ.get("MOCK_LATENCY_MS_MIN", "10000"))
LATENCY_MAX = int(os.environ.get("MOCK_LATENCY_MS_MAX", "15000"))
PORT = int(os.environ.get("MOCK_PORT", "18080"))
TOKEN = os.environ.get("MOCK_TOKEN", "fpslot-mock")
FAILURE_RATE = float(os.environ.get("MOCK_FAILURE_RATE", "0.0"))


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
    body = await request.json()
    model = body.get("model", "unknown")
    messages = body.get("messages", [])
    is_stream = body.get("stream", False)

    user_last = ""
    for m in messages:
        if m.get("role") == "user":
            user_last = m.get("content", "")
    if isinstance(user_last, list):
        user_last = json.dumps(user_last)

    # Simulate upstream "thinking" time. Random within configured range so
    # concurrent requests don't all wake up at the same instant (closer to
    # real LLM upstream behavior under load).
    delay_ms = random.randint(LATENCY_MIN, LATENCY_MAX)
    await asyncio.sleep(delay_ms / 1000.0)

    # Optional synthetic failures for fault-injection testing.
    if random.random() < FAILURE_RATE:
        elapsed = (time.monotonic() - started) * 1000
        print(f"[{TOKEN}] req={request_id} FAIL after {elapsed:.0f}ms (latency_budget={delay_ms}ms)",
              flush=True)
        return web.json_response(
            {"error": {"message": "mock synthetic 500", "type": "server_error", "code": "internal"}},
            status=500,
        )

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
            "choices": [
                {"index": 0, "delta": {}, "finish_reason": "stop"}
            ],
        }
        await response.write(f"data: {json.dumps(final)}\n\n".encode("utf-8"))
        await response.write(b"data: [DONE]\n\n")

        elapsed = (time.monotonic() - started) * 1000
        print(f"[{TOKEN}] req={request_id} STREAM done after {elapsed:.0f}ms "
              f"(budget={delay_ms}ms, model={model})",
              flush=True)
        return response

    body_out = make_response(model, content, request_id)
    # NOTE: do NOT add custom top-level fields like x_request_id to the body.
    # The gateway's StripMinimaxFieldsBody hook strips non-standard fields
    # from the upstream response, which would cause Content-Length to
    # disagree with the actual body bytes (client gets IncompleteRead).
    # The X-Request-Id response HEADER is still set below for log correlation.
    elapsed = (time.monotonic() - started) * 1000
    print(f"[{TOKEN}] req={request_id} OK after {elapsed:.0f}ms "
          f"(budget={delay_ms}ms, model={model})",
          flush=True)
    return web.json_response(body_out, headers={"X-Request-Id": request_id})


async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response({
        "status": "ok",
        "token": TOKEN,
        "latency_ms_range": [LATENCY_MIN, LATENCY_MAX],
        "failure_rate": FAILURE_RATE,
    })


async def handle_models(_request: web.Request) -> web.Response:
    """OpenAI-compatible /v1/models endpoint.

    The gateway's credential cycler probes this to verify the upstream
    is alive. Without it the probe gets 404 and marks the credential
    health_status='unreachable', which makes is_routable=false.
    """
    return web.json_response({
        "object": "list",
        "data": [
            {"id": "gpt-4o", "object": "model", "created": int(time.time()), "owned_by": "local-mock"},
            {"id": "gpt-4o-mini", "object": "model", "created": int(time.time()), "owned_by": "local-mock"},
        ],
    })


def build_app() -> web.Application:
    app = web.Application()
    app.router.add_post("/v1/chat/completions", handle_chat)
    app.router.add_get("/v1/models", handle_models)
    app.router.add_get("/healthz", handle_health)
    app.router.add_get("/", handle_health)
    return app


if __name__ == "__main__":
    print(f"[{TOKEN}] starting mock upstream on :{PORT}, "
          f"latency={LATENCY_MIN}-{LATENCY_MAX}ms, "
          f"failure_rate={FAILURE_RATE}",
          flush=True)
    web.run_app(build_app(), host="0.0.0.0", port=PORT, print=None)