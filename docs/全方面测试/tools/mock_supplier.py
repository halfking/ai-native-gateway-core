#!/usr/bin/env python3
# docs/全方面测试/tools/mock_supplier.py
#
# 单进程单端口的 mock LLM 供应商 — 支持通过 HTTP admin 切换故障状态。
#
# 设计：12 组（A-L）× 5 实例 = 60 个 mock_supplier 进程（端口 19080-19139），
# 每个由 mock_orchestrator.py 控制（按 group 批量发布 set-state）。
#
# 支持的状态：
#   - healthy       : 正常 200 OK
#   - slow          : 2-4s 延迟
#   - flaky         : 30% 错误率
#   - server_error  : 100% 500
#   - auth          : 100% 401
#   - quota_429     : 配额耗尽后 429
#   - dropped       : 503 dropped（断开候选链用）
#   - broken_stream : SSE 写一半断流
#   - context_too_long: 413
#
# 协议模式 (protocol_mode):
#   - chat      : OpenAI Chat Completions 格式
#   - response  : Anthropic Response API 格式
#   - anthropic : Anthropic Messages API 格式
#
# 边缘故障模式:
#   - slow_connect_delay_ms   : TCP握手延迟
#   - timeout_response        : 模拟超时
#   - huge_response           : >10MB 响应
#   - truncated_response       : 响应截断
#   - slow_header_delay_ms    : 头部发送延迟
#   - invalid_json_response    : 返回无效JSON
#
# 用法：
#   python3 mock_supplier.py --port 19080 --group A --instance 0
#
# 控制：
#   GET  /healthz
#   GET  /v1/models
#   POST /v1/chat/completions
#   POST /admin/state         {"state": "slow"}
#   POST /admin/profile       {"latency_ms_extra": 1000, "latency_prob": 0.3}
#   POST /admin/quota         {"tokens": 2000, "window_sec": 60}
#   POST /admin/protocol      {"protocol": "chat|response|anthropic"}
#   POST /admin/delay         {"delay_ms": 500}
#   POST /admin/connlimit     {"limit": 10}
#   POST /admin/fault-mode    {"timeout_response": true, "huge_response": false}
#   POST /admin/state-full    {"state": "slow", "latency_ms_extra": 1000}
#   POST /admin/reset

import argparse
import asyncio
import copy
import json
import os
import random
import sys
import time
import uuid

from aiohttp import web


STATE_DEFAULTS = {
    "state": "healthy",
    "latency_ms_extra": 0,
    "latency_prob": 0,
    "fail_rate": 0,
    "quota_tokens": 1_000_000,
    "quota_window_sec": 60,
    "quota_consumed": 0,
    "state_change_at": 0.0,
    "quota_window_start_at": 0.0,
    "broken_stream_drop_after": 999_999,
    # Protocol mode support (chat=OpenAI, response=Anthropic, anthropic=Messages API)
    "protocol_mode": "chat",
    # Processing delay (applied after queue, before response)
    "processing_delay_ms": 0,
    # Connection limiting (0 = unlimited)
    "max_connections": 0,
    # Edge case fault modes
    "slow_connect_delay_ms": 0,      # TCP handshake delay
    "timeout_response": False,         # Simulate timeout
    "huge_response": False,          # >10MB response
    "truncated_response": False,     # Response cut off mid-stream
    "slow_header_delay_ms": 0,       # Header send delay
    "invalid_json_response": False,   # Return invalid JSON
    # 2026-08-06: scripted-content for deterministic LLM responses.
    # When set (non-empty string), /v1/chat/completions returns this exact
    # content instead of the default `[group/instance] mock-pong: ...` echo.
    # Used by S20 (auto-title), S22 (instant summary), S23 (long-text)
    # to assert on deterministic title/summary text in DB.
    "scripted_content": "",
    "scripted_model_override": "",    # optional: override the `model` field in response
}

STATE: dict = dict(STATE_DEFAULTS)

# Connection manager for concurrency limiting
class ConnectionManager:
    def __init__(self):
        self._count = 0
        self._limit = 0
        self._condition = asyncio.Condition()

    async def set_limit(self, limit: int):
        """Set max concurrent connections (0 = unlimited)."""
        async with self._condition:
            self._limit = max(0, int(limit))
            self._condition.notify_all()

    async def try_acquire(self):
        """Acquire a slot without waiting, returning False when limited."""
        async with self._condition:
            if self._limit > 0 and self._count >= self._limit:
                return False
            self._count += 1
        return True

    async def acquire(self):
        """Acquire a connection slot (blocks if at limit)."""
        async with self._condition:
            while self._limit > 0 and self._count >= self._limit:
                await self._condition.wait()
            self._count += 1
        return True

    async def release(self):
        """Release a connection slot."""
        async with self._condition:
            self._count = max(0, self._count - 1)
            self._condition.notify_all()

    @property
    def current(self) -> int:
        return self._count


CONN_MGR = ConnectionManager()


class Args:
    group: str = "A"
    instance: int = 0
    host: str = "127.0.0.1"


ARGS = Args()
STARTED_AT = 0.0
REQUESTS_TOTAL = 0
REQUESTS_2XX = 0
REQUESTS_5XX = 0


def now() -> float:
    return time.time()


def state_age() -> float:
    return now() - STATE.get("state_change_at", now())


def quota_tick_if_needed():
    if STATE["quota_window_sec"] <= 0:
        return
    if now() - STATE["quota_window_start_at"] > STATE["quota_window_sec"]:
        STATE["quota_consumed"] = 0
        STATE["quota_window_start_at"] = now()


# ── Admin endpoints ─────────────────────────────────────────────────────────


async def healthz(request):
    return web.json_response(
        {
            "ok": True,
            "group": ARGS.group,
            "instance": ARGS.instance,
            "state": STATE["state"],
            "state_age_sec": state_age(),
            "uptime_sec": now() - STARTED_AT,
            "requests_total": REQUESTS_TOTAL,
            "requests_2xx": REQUESTS_2XX,
            "requests_5xx": REQUESTS_5XX,
        }
    )


async def admin_get_state(request):
    return web.json_response(
        {
            "group": ARGS.group,
            "instance": ARGS.instance,
            "state": dict(STATE),
            "uptime_sec": now() - STARTED_AT,
        }
    )


async def admin_set_state(request):
    body = await request.json()
    if "state" not in body:
        return web.json_response({"error": "missing state"}, status=400)
    STATE["state"] = body["state"]
    STATE["state_change_at"] = now()
    return web.json_response({"ok": True, "applied": STATE["state"]})


async def admin_set_profile(request):
    body = await request.json()
    for k in (
        "latency_ms_extra",
        "latency_prob",
        "fail_rate",
        "quota_tokens",
        "quota_window_sec",
        "quota_consumed",
        "broken_stream_drop_after",
        # Edge case fault modes
        "slow_connect_delay_ms",
        "timeout_response",
        "huge_response",
        "truncated_response",
        "slow_header_delay_ms",
        "invalid_json_response",
    ):
        if k in body:
            STATE[k] = body[k]
    STATE["state_change_at"] = now()
    return web.json_response(
        {"ok": True, "applied": {k: STATE[k] for k in STATE if k in body}}
    )


async def admin_set_quota(request):
    body = await request.json()
    if "tokens" in body:
        STATE["quota_tokens"] = int(body["tokens"])
    if "window_sec" in body:
        STATE["quota_window_sec"] = int(body["window_sec"])
    STATE["quota_consumed"] = int(body.get("consumed", 0))
    STATE["quota_window_start_at"] = now()
    return web.json_response(
        {"ok": True, "quota": {k: STATE[k] for k in STATE if k.startswith("quota_")}}
    )


async def admin_reset(request):
    STATE.clear()
    STATE.update(copy.deepcopy(STATE_DEFAULTS))
    STATE["state_change_at"] = now()
    STATE["quota_window_start_at"] = now()
    await CONN_MGR.set_limit(0)
    return web.json_response({"ok": True, "message": "reset"})


async def admin_set_protocol(request):
    """Set protocol_mode: chat|response|anthropic"""
    body = await request.json()
    protocol = body.get("protocol", "chat")
    if protocol not in ("chat", "response", "anthropic"):
        return web.json_response({"error": "protocol must be chat|response|anthropic"}, status=400)
    STATE["protocol_mode"] = protocol
    STATE["state_change_at"] = now()
    return web.json_response({"ok": True, "protocol_mode": protocol})


async def admin_set_delay(request):
    """Set processing_delay_ms"""
    body = await request.json()
    delay_ms = int(body.get("delay_ms", 0))
    STATE["processing_delay_ms"] = max(0, delay_ms)
    STATE["state_change_at"] = now()
    return web.json_response({"ok": True, "processing_delay_ms": STATE["processing_delay_ms"]})


async def admin_set_connlimit(request):
    """Set max_connections (0 = unlimited)"""
    body = await request.json()
    limit = int(body.get("limit", 0))
    STATE["max_connections"] = max(0, limit)
    await CONN_MGR.set_limit(limit)
    STATE["state_change_at"] = now()
    return web.json_response({"ok": True, "max_connections": limit, "current": CONN_MGR.current})


async def admin_set_fault_mode(request):
    """Set edge case fault modes:
    - slow_connect_delay_ms: TCP handshake delay (ms)
    - timeout_response: simulate timeout
    - huge_response: >10MB response
    - truncated_response: cut off mid-stream
    - slow_header_delay_ms: header send delay
    - invalid_json_response: return invalid JSON
    """
    body = await request.json()
    fault_modes = [
        "slow_connect_delay_ms",
        "timeout_response",
        "huge_response",
        "truncated_response",
        "slow_header_delay_ms",
        "invalid_json_response",
    ]
    for k in fault_modes:
        if k in body:
            if k in ("timeout_response", "huge_response", "truncated_response", "invalid_json_response"):
                STATE[k] = bool(body[k])
            else:
                STATE[k] = max(0, int(body[k]))
    STATE["state_change_at"] = now()
    applied = {k: STATE[k] for k in fault_modes if k in body}
    return web.json_response({"ok": True, "applied": applied})


async def admin_set_state_full(request):
    """Set multiple state fields at once (convenience endpoint)"""
    body = await request.json()
    valid_fields = {
        "state", "latency_ms_extra", "latency_prob", "fail_rate",
        "broken_stream_drop_after", "protocol_mode", "processing_delay_ms",
        "max_connections", "slow_connect_delay_ms", "timeout_response",
        "huge_response", "truncated_response", "slow_header_delay_ms",
        "invalid_json_response"
    }
    for k, v in body.items():
        if k in valid_fields:
            if isinstance(v, bool):
                STATE[k] = v
            elif isinstance(v, int):
                STATE[k] = v
            elif isinstance(v, float):
                STATE[k] = v
            elif isinstance(v, str):
                STATE[k] = v
    STATE["state_change_at"] = now()
    if "max_connections" in body:
        await CONN_MGR.set_limit(body["max_connections"])
    return web.json_response({"ok": True, "applied": {k: STATE[k] for k in body.keys() if k in valid_fields}})


# 2026-08-06: scripted-response admin endpoint. When `scripted_content` is set
# (non-empty), chat completions return this content verbatim instead of the
# default echo. Used by S20/S22/S23 to assert on deterministic LLM text.
async def admin_set_scripted_response(request):
    try:
        body = await request.json()
    except Exception:
        return web.json_response({"error": "invalid json"}, status=400)
    if "content" not in body:
        return web.json_response({"error": "missing content"}, status=400)
    if not isinstance(body["content"], str):
        return web.json_response({"error": "content must be string"}, status=400)
    STATE["scripted_content"] = body["content"]
    if "model_override" in body and isinstance(body["model_override"], str):
        STATE["scripted_model_override"] = body["model_override"]
    STATE["state_change_at"] = now()
    return web.json_response({
        "ok": True,
        "applied": {
            "scripted_content": STATE["scripted_content"],
            "scripted_model_override": STATE["scripted_model_override"],
        },
    })


# ── Business endpoints ─────────────────────────────────────────────────────


async def chat_completions(request):
    global REQUESTS_TOTAL, REQUESTS_2XX, REQUESTS_5XX
    REQUESTS_TOTAL += 1
    quota_tick_if_needed()

    if not await CONN_MGR.try_acquire():
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "connection_limit", "message": "too many connections"}},
            status=503,
        )

    try:
        return await _do_chat_completions(request)
    finally:
        await CONN_MGR.release()


async def _do_chat_completions(request):
    """Internal handler (connection already acquired)."""
    global REQUESTS_TOTAL, REQUESTS_2XX, REQUESTS_5XX

    # aiohttp cannot delay the TCP accept itself; this models a slow upstream
    # before request processing begins.
    if STATE["slow_connect_delay_ms"] > 0:
        await asyncio.sleep(STATE["slow_connect_delay_ms"] / 1000.0)

    s = STATE["state"]
    if s == "rate_limited":
        s = "quota_429"
    body = {}
    try:
        body = await request.json()
    except Exception:
        pass

    # Auth/quota/server_error/dropped hard failures
    if STATE["timeout_response"]:
        await asyncio.sleep(3600)
    if s == "dropped":
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "dropped", "message": "mock dropped"}}, status=503
        )
    if s == "server_error":
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "server", "message": "chaos server_error"}}, status=500
        )
    if s == "auth":
        return web.json_response(
            {"error": {"type": "auth", "message": "chaos 401"}}, status=401
        )
    if s == "context_too_long":
        return web.json_response(
            {"error": {"type": "context_length", "message": "context too long"}},
            status=413,
        )
    if s == "quota_429" and STATE["quota_consumed"] >= STATE["quota_tokens"]:
        return web.json_response(
            {
                "error": {
                    "type": "rate_limit",
                    "code": "insufficient_quota",
                    "message": "quota exhausted",
                }
            },
            status=429,
        )

    # Latency
    delay_ms = 0
    if STATE["latency_ms_extra"] > 0 and random.random() < STATE["latency_prob"]:
        delay_ms += STATE["latency_ms_extra"]
    if s == "slow":
        delay_ms += random.randint(2000, 4000)
    # Processing delay (deterministic, added after other latency)
    delay_ms += STATE["processing_delay_ms"]
    if delay_ms > 0:
        await asyncio.sleep(delay_ms / 1000.0)

    # Random failure rate
    if STATE["fail_rate"] > 0 and random.random() < STATE["fail_rate"]:
        rc = random.choice([500, 502, 503])
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "flaky", "message": f"chaos flaky {rc}"}}, status=rc
        )
    if s == "flaky" and random.random() < 0.3:
        rc = random.choice([500, 503])
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "flaky", "message": f"chaos flaky {rc}"}}, status=rc
        )

    # Charge quota if in quota-mode
    if s == "quota_429":
        p_tokens = sum(
            len(str(m.get("content", ""))) // 4 for m in body.get("messages", [])
        )
        c_tokens = body.get("max_tokens", 50)
        STATE["quota_consumed"] += p_tokens + c_tokens
        if STATE["quota_consumed"] > STATE["quota_tokens"]:
            return web.json_response(
                {
                    "error": {
                        "type": "rate_limit",
                        "code": "insufficient_quota",
                        "message": "quota exhausted",
                    }
                },
                status=429,
            )

    cid = f"mock-{uuid.uuid4().hex[:24]}"
    ts = int(time.time())
    model = body.get("model", "loadtest-default")
    msgs = body.get("messages", []) or []
    last_user = ""
    for m in reversed(msgs):
        if m.get("role") in ("user", "tool"):
            last_user = str(m.get("content", ""))
            break
    reply = f"[{ARGS.group}/{ARGS.instance}] mock-pong: {last_user[:140]}".replace(
        "\n", " "
    )

    # 2026-08-06: scripted-content override (S20/S22/S23 deterministic LLM response).
    # When set, return the scripted content verbatim. This bypasses the echo
    # template so callers can assert on exact title/summary text.
    if STATE.get("scripted_content", ""):
        reply = STATE["scripted_content"]
    if STATE.get("scripted_model_override", ""):
        model = STATE["scripted_model_override"]

    if STATE["huge_response"]:
        reply = reply + (" x" * (6 * 1024 * 1024))

    protocol = STATE.get("protocol_mode", "chat")

    if STATE["invalid_json_response"]:
        return web.Response(status=200, body=b"{invalid-json", content_type="application/json")

    if STATE["truncated_response"] and not body.get("stream"):
        return web.Response(
            status=200,
            body=b'{"id":"' + cid.encode() + b'","choices":[',
            content_type="application/json",
        )

    if STATE["slow_header_delay_ms"] > 0:
        await asyncio.sleep(STATE["slow_header_delay_ms"] / 1000.0)

    if body.get("stream"):
        resp = web.StreamResponse(
            status=200,
            headers={
                "Content-Type": "text/event-stream",
                "Cache-Control": "no-cache",
                "X-Accel-Buffering": "no",
            },
        )
        await resp.prepare(request)

        # broken_stream/truncated_response: write a prefix, then close early
        if s == "broken_stream" or STATE["truncated_response"]:
            STATE["broken_stream_drop_after"] = min(5, len(reply))

        sent = 0
        for ch in reply:
            if sent >= STATE["broken_stream_drop_after"]:
                await response_break(resp)
                return resp
            chunk = format_chunk(cid, model, ch, ts, protocol, index=0)
            await resp.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
            sent += 1

        # Final chunk
        chunk = format_chunk_final(cid, model, ts, protocol, index=0)
        await resp.write(b"data: " + json.dumps(chunk).encode() + b"\n\n")
        await resp.write(b"data: [DONE]\n\n")
        await resp.write_eof()
        REQUESTS_2XX += 1
        return resp

    REQUESTS_2XX += 1
    return web.json_response(
        format_response(cid, model, reply, ts, protocol),
        status=200,
    )


def format_response(cid: str, model: str, content: str, ts: int, protocol: str, **kwargs):
    """Format response based on protocol mode."""
    usage = {"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70}
    if protocol == "response":
        # Anthropic response API format
        return {
            "id": cid,
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": content}],
            "model": model,
            "usage": usage,
        }
    elif protocol == "anthropic":
        # Anthropic Messages API format
        return {
            "id": cid,
            "type": "message",
            "role": "assistant",
            "content": [{"type": "text", "text": content}],
            "model": model,
            "usage": usage,
        }
    else:
        # OpenAI chat completions format (default)
        return {
            "id": cid,
            "object": "chat.completion",
            "created": ts,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": content},
                    "finish_reason": "stop",
                    "logprobs": None,
                }
            ],
            "usage": usage,
        }


def format_chunk(cid: str, model: str, content: str, ts: int, protocol: str, **kwargs):
    """Format streaming chunk based on protocol mode."""
    if protocol == "response":
        # Anthropic response API streaming
        return {
            "type": "content_block_delta",
            "index": kwargs.get("index", 0),
            "delta": {"type": "text", "text": content},
        }
    elif protocol == "anthropic":
        # Anthropic Messages API streaming
        return {
            "type": "content_block_delta",
            "index": kwargs.get("index", 0),
            "delta": {"type": "text_delta", "text": content},
        }
    else:
        # OpenAI chat completions streaming (default)
        return {
            "id": cid,
            "object": "chat.completion.chunk",
            "created": ts,
            "model": model,
            "choices": [
                {
                    "index": kwargs.get("index", 0),
                    "delta": {"content": content},
                    "finish_reason": None,
                }
            ],
        }


def format_chunk_final(cid: str, model: str, ts: int, protocol: str, **kwargs):
    """Format final streaming chunk (stop reason)."""
    if protocol in ("response", "anthropic"):
        return {"type": "message_stop"}
    else:
        return {
            "id": cid,
            "object": "chat.completion.chunk",
            "created": ts,
            "model": model,
            "choices": [{"index": kwargs.get("index", 0), "delta": {}, "finish_reason": "stop"}],
        }


async def response_break(resp):
    """模拟 broken_stream — 写一半断开（不上 EOF）。"""
    try:
        await resp.write_eof()
    except Exception:
        pass


async def list_models(request):
    models = [
        "minimax-m3",
        "gpt-5.6-luna",
        "gpt-5.6-terra",
        "claude-sonnet-5",
        "gpt-4o",
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
        "chaos-503",
        "chaos-429",
        "chaos-slow",
        "chaos-mid_drop",
        "chaos-tool_bug",
    ]
    ts = int(time.time())
    data = [
        {
            "id": m,
            "object": "model",
            "created": ts,
            "owned_by": f"mock-{ARGS.group}-{ARGS.instance}",
        }
        for m in models
    ]
    return web.json_response({"object": "list", "data": data})


# ── Wiring ─────────────────────────────────────────────────────────────────


def build_app():
    app = web.Application()
    app.router.add_get("/healthz", healthz)
    app.router.add_get("/v1/models", list_models)
    app.router.add_post("/v1/chat/completions", chat_completions)
    app.router.add_get("/admin/state", admin_get_state)
    app.router.add_post("/admin/state", admin_set_state)
    app.router.add_post("/admin/profile", admin_set_profile)
    app.router.add_post("/admin/quota", admin_set_quota)
    app.router.add_post("/admin/reset", admin_reset)
    app.router.add_post("/admin/protocol", admin_set_protocol)
    app.router.add_post("/admin/delay", admin_set_delay)
    app.router.add_post("/admin/connlimit", admin_set_connlimit)
    app.router.add_post("/admin/fault-mode", admin_set_fault_mode)
    app.router.add_post("/admin/state-full", admin_set_state_full)
    app.router.add_post("/admin/scripted-response", admin_set_scripted_response)
    return app


def main():
    global ARGS, STARTED_AT
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default=os.getenv("MOCK_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument(
        "--group", default="A", help="组名 A-L，决定 5 个共享状态机的实例"
    )
    parser.add_argument("--instance", type=int, default=0, help="组内实例 0-4")
    parser.add_argument("--state", default=os.getenv("INIT_STATE", "healthy"))
    parser.add_argument("--quiet", action="store_true")
    ARGS = parser.parse_args()

    STATE["state"] = ARGS.state
    STATE["state_change_at"] = now()
    STATE["quota_window_start_at"] = now()
    STARTED_AT = now()

    if not ARGS.quiet:
        print(
            f"[{ARGS.group}/{ARGS.instance}] listening on http://{ARGS.host}:{ARGS.port} "
            f"state={ARGS.state}",
            file=sys.stderr,
            flush=True,
        )

    web.run_app(
        build_app(), host=ARGS.host, port=ARGS.port, access_log=None, print=None
    )


if __name__ == "__main__":
    main()
