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
}

STATE: dict = dict(STATE_DEFAULTS)


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
    return web.json_response({"ok": True, "message": "reset"})


# ── Business endpoints ─────────────────────────────────────────────────────


async def chat_completions(request):
    global REQUESTS_TOTAL, REQUESTS_2XX, REQUESTS_5XX
    REQUESTS_TOTAL += 1
    quota_tick_if_needed()

    s = STATE["state"]
    body = {}
    try:
        body = await request.json()
    except Exception:
        pass

    # Auth/quota/server_error/dropped hard failures
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

        # broken_stream: 写一段就断
        if s == "broken_stream":
            STATE["broken_stream_drop_after"] = 5

        sent = 0
        for ch in reply:
            if sent >= STATE["broken_stream_drop_after"]:
                await response_break(resp)
                return resp
            await resp.write(
                b"data: "
                + json.dumps(
                    {
                        "id": cid,
                        "object": "chat.completion.chunk",
                        "created": ts,
                        "model": model,
                        "choices": [
                            {
                                "index": 0,
                                "delta": {"content": ch},
                                "finish_reason": None,
                            }
                        ],
                    }
                ).encode()
                + b"\n\n"
            )
            sent += 1

        await resp.write(
            b"data: "
            + json.dumps(
                {
                    "id": cid,
                    "object": "chat.completion.chunk",
                    "created": ts,
                    "model": model,
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                }
            ).encode()
            + b"\n\n"
        )
        await resp.write(b"data: [DONE]\n\n")
        await resp.write_eof()
        REQUESTS_2XX += 1
        return resp

    REQUESTS_2XX += 1
    return web.json_response(
        {
            "id": cid,
            "object": "chat.completion",
            "created": ts,
            "model": model,
            "choices": [
                {
                    "index": 0,
                    "message": {"role": "assistant", "content": reply},
                    "finish_reason": "stop",
                    "logprobs": None,
                }
            ],
            "usage": {"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70},
        }
    )


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
