# tests/local/mock-llm/server.py
#
# OpenAI-compatible upstream mock — 0 依赖（用标准库）。
# 设计目标：用确定性故障注入重现 2026-07-12 minimax-m3 各种错误路径，
# 配合 gateway 的 circuit / fp_slot / probe / degradation 模块做端到端验证。
#
# Usage:
#   python3 server.py --host 0.0.0.0 --port 8000
#
# 故障注入模型（按 model 名前缀或 query string）：
#   <MODEL_FAULT_PROFILES>

import argparse
import asyncio
import base64
import hashlib
import json
import os
import random
import re
import sys
import time
import uuid

# === 解析 CLI args early（让 SIGTERM/PIPE 干净） ===
parser = argparse.ArgumentParser()
parser.add_argument("--host", default=os.getenv("MOCK_HOST", "0.0.0.0"))
parser.add_argument("--port", type=int, default=int(os.getenv("MOCK_PORT", "8000")))
parser.add_argument(
    "--chaos",
    action="store_true",
    help="启用 -chaos: 模型名带前缀触发故障模式 (drop_*, slow_*, etc.)",
)
args = parser.parse_args()


# === Bootstrap aiohttp lazily ===
try:
    from aiohttp import web
except ImportError:
    print("ERROR: aiohttp missing — install via: pip3 install aiohttp", file=sys.stderr)
    sys.exit(1)


# ── 故障注入策略 ──────────────────────────────────────────────────────────
#
# 每一类故障都是可组合的小函数（pre/post stream），通过 HTTP handler 链式调用。
# 可以通过 URL 参数即时启用，也可通过模型名前缀（如果启动 --chaos）启用。

CHAOS_PROFILES = {
    # === Status codes ===
    "400": {
        "status": 400,
        "body": {"error": {"type": "invalid_request", "message": "chaos 400"}},
    },
    "401": {"status": 401, "body": {"error": {"type": "auth", "message": "chaos 401"}}},
    "403": {
        "status": 403,
        "body": {"error": {"type": "auth", "message": "chaos 403 (billing)"}},
    },
    "404": {
        "status": 404,
        "body": {
            "error": {"type": "not_found", "message": "InvalidEndpointOrModel.NotFound"}
        },
    },
    "413": {
        "status": 413,
        "body": {"error": {"type": "context_length", "message": "context too long"}},
    },
    "429": {
        "status": 429,
        "body": {"error": {"type": "rate_limit", "message": "rate limit exceeded"}},
    },
    "500": {
        "status": 500,
        "body": {"error": {"type": "server", "message": "chaos 500"}},
    },
    "502": {
        "status": 502,
        "body": {"error": {"type": "upstream", "message": "chaos 502"}},
    },
    "503": {
        "status": 503,
        "body": {"error": {"type": "unavailable", "message": "Service Unavailable"}},
    },
    # === Streaming chaos ===
    "mid_drop": {"streaming_drop_after": 5},
    "drop_done": {"streaming_skip_done": True},
    "slow_chunk": {"streaming_chunk_interval_ms": 500},
    "super_slow": {"streaming_first_chunk_ms": 3000},
    # === Latency ===
    "fast": {"latency_ms": 10},
    "slow": {"latency_ms": 1500},
    "very_slow": {"latency_ms": 5000},
    # === Body chaos ===
    "garbled": {
        "content_prefix": "💥[tool_call_id_mismatch] tool result's tool id(functions.read:265) not found (2013)"
    },
    "tool_bug": {
        "content_prefix": '{"type":"error","error":{"type":"bad_request_error","message":"invalid params, tool result\'s tool id(functions.read:265) not found (2013)","http_code":"400"}}'
    },
}


# ── 业务 helper ───────────────────────────────────────────────────────────


def hash_credential(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()[:8]


def make_completion_id() -> str:
    return f"mock-{uuid.uuid4().hex[:24]}"


def last_user_message(messages: list) -> str:
    return next(
        (
            m.get("content", "")
            for m in reversed(messages)
            if m.get("role") in ("user", "tool")
        ),
        "",
    )


def build_pong_reply(model: str, last_user: str) -> str:
    base = f"[{model}] pong:"
    snippet = (last_user or "")[:160].replace("\n", " ")
    return f"{base} {snippet}"


# ── Handlers ───────────────────────────────────────────────────────────────


async def healthz(request):
    return web.Response(text="ok", status=200, headers={"Content-Type": "text/plain"})


async def list_models(request):
    payload = {
        "object": "list",
        "data": [
            {
                "id": m,
                "object": "model",
                "created": int(time.time()),
                "owned_by": "mock",
            }
            for m in [
                # 健康对照组 — 应该 200 OK
                "minimax-m3",
                "gpt-5.6-luna",
                "gpt-5.6-terra",
                "claude-sonnet-5",
                "gpt-4o",
                # 故障注入 — 模型名带前缀触发 CHAOS_PROFILES 中的对应行为
                *sorted(f"chaos-{name}" for name in CHAOS_PROFILES.keys()),
            ]
        ],
    }
    return web.json_response(payload)


async def chat_completions(request):
    try:
        body = await request.json()
    except Exception:
        return web.json_response(
            {"error": {"type": "invalid_request", "message": "malformed json"}},
            status=400,
        )

    model = body.get("model", "minimax-m3")
    messages = body.get("messages", [])
    want_stream = bool(body.get("stream", False))
    max_tokens = int(body.get("max_tokens", 50))

    # 1. 解析故障来源（URL 参数 > --chaos 模型前缀）
    forced = {}
    qs = dict(request.query)
    if "force_status" in qs and qs["force_status"] in CHAOS_PROFILES:
        forced.update(CHAOS_PROFILES[qs["force_status"]])
    if "latency_ms" in qs:
        forced["latency_ms"] = int(qs["latency_ms"])

    if args.chaos:
        for prefix, profile in CHAOS_PROFILES.items():
            if model == f"chaos-{prefix}" or model.startswith(f"chaos-{prefix}-"):
                forced.update(profile)
                break

    # 2. delay 模拟上游延迟
    if "latency_ms" in forced:
        await asyncio.sleep(forced["latency_ms"] / 1000.0)

    # 3. forced status code (短路)
    if "status" in forced:
        body_obj = forced.get("body", {"error": {"type": "chaos", "message": "forced"}})
        return web.json_response(body_obj, status=forced["status"])

    # 4. 实际生成响应
    cid = make_completion_id()
    now = int(time.time())
    last_user = last_user_message(messages)
    reply = build_pong_reply(model, last_user)

    # 4a. content 注入
    if "content_prefix" in forced:
        reply = forced["content_prefix"] + " " + reply

    # 4b. streaming
    if want_stream:
        return await stream_response(
            request, cid, now, model, reply, max_tokens, forced
        )

    # 4c. 非流式
    return web.json_response(
        {
            "id": cid,
            "object": "chat.completion",
            "created": now,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": reply},
                    "finish_reason": "stop",
                    "logprobs": None,
                }
            ],
            "usage": {
                "prompt_tokens": sum(
                    len(m.get("content", "").split()) for m in messages
                ),
                "completion_tokens": min(max_tokens, len(reply.split())),
                "total_tokens": sum(len(m.get("content", "").split()) for m in messages)
                + min(max_tokens, len(reply.split())),
            },
        }
    )


async def stream_response(
    request,
    cid: str,
    now: int,
    model: str,
    reply: str,
    max_tokens: int,
    forced: dict,
):
    """SSE 响应 — 支持 drop_done / mid_drop / slow_chunk / super_slow 等。"""
    first_chunk_ms = forced.get("streaming_first_chunk_ms", 0)
    if first_chunk_ms:
        await asyncio.sleep(first_chunk_ms / 1000.0)

    chunk_interval = forced.get("streaming_chunk_interval_ms", 30) / 1000.0
    drop_after = forced.get("streaming_drop_after", 999_999)
    skip_done = forced.get("streaming_skip_done", False)

    response = web.StreamResponse(
        status=200,
        headers={
            "Content-Type": "text/event-stream",
            "Cache-Control": "no-cache",
            "X-Accel-Buffering": "no",
        },
    )
    await response.prepare(request=request)

    async def send(obj):
        await response.write(b"data: " + json.dumps(obj).encode() + b"\n\n")

    sent = 0
    for ch in reply:
        if sent >= drop_after:
            # mid_drop: 写到一半直接关连接，模拟 upstream EOF without [DONE]
            await response.write_eof()
            return response
        await send(
            {
                "id": cid,
                "object": "chat.completion.chunk",
                "created": now,
                "model": model,
                "choices": [
                    {"index": 0, "delta": {"content": ch}, "finish_reason": None}
                ],
            }
        )
        sent += 1
        if chunk_interval > 0:
            await asyncio.sleep(chunk_interval)

    # tail chunk
    await send(
        {
            "id": cid,
            "object": "chat.completion.chunk",
            "created": now,
            "model": model,
            "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
        }
    )

    if not skip_done:
        await response.write(b"data: [DONE]\n\n")

    await response.write_eof()
    return response


# ── App wiring ─────────────────────────────────────────────────────────────


def build_app():
    app = web.Application()
    app.router.add_get("/v1/models", list_models)
    app.router.add_post("/v1/chat/completions", chat_completions)
    app.router.add_get("/healthz", healthz)
    return app


def main():
    print(f"mock-llm listening on http://{args.host}:{args.port}", file=sys.stderr)
    print(f"  --chaos={'ON' if args.chaos else 'OFF'}", file=sys.stderr)
    if args.chaos:
        print(f"  Chaos profiles enabled: {len(CHAOS_PROFILES)}", file=sys.stderr)
        print(
            f'    e.g. POST /v1/chat/completions {{"model": "chaos-503", ...}}',
            file=sys.stderr,
        )
        print(f"    or use query: ?force_status=429&latency_ms=2000", file=sys.stderr)

    web.run_app(
        build_app(), host=args.host, port=args.port, access_log=None, print=None
    )


if __name__ == "__main__":
    main()
