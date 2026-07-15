#!/usr/bin/env python3
# docs/全方面测试/tools/multimodal_supplier.py
#
# Enhanced mock supplier with multimodal-aware request inspection.
#
# Extends the base mock_supplier.py to:
#   1. Validate the request's content structure (image_url / image / input_audio / file)
#   2. Record the *exact* body parts the gateway forwarded to us
#      (so the test can assert "the image_url block was preserved through
#       the gateway's pipeline, no data-URI was redacted, etc.")
#   3. Capture X-Device-Seed / X-Machine-Id / X-Client-Profile / X-Tenant-Id
#      headers (so we can verify client fingerprint and tenant labeling)
#   4. Expose /admin/assertions for the test client to query the last N
#      received requests
#   5. /v1/messages endpoint with Anthropic-style multimodal (image blocks)
#
# Run alongside the regular mock_supplier.py — the multimodal scenarios
# S17-S22 route to these specific instances (port 19280-19284) so we
# can match expected vs received in a deterministic way.

import argparse
import asyncio
import base64
import copy
import hashlib
import json
import os
import random
import sys
import time
import uuid
from collections import deque
from typing import Any, Dict, List, Optional

from aiohttp import web

STATE_DEFAULTS: Dict[str, Any] = {
    "state": "healthy",
    "latency_ms_extra": 0,
    "latency_prob": 0.0,
    "fail_rate": 0.0,
    "quota_tokens": 1_000_000,
    "quota_window_sec": 60,
    "quota_consumed": 0,
    "state_change_at": 0.0,
    "quota_window_start_at": 0.0,
    "broken_stream_drop_after": 999_999,
}

STATE: dict = dict(STATE_DEFAULTS)


class Args:
    group: str = "MM"
    instance: int = 0
    host: str = "127.0.0.1"
    port: int = 0
    state: str = "healthy"
    quiet: bool = False


ARGS = Args()
STARTED_AT: float = 0.0
REQUESTS_TOTAL: int = 0
REQUESTS_2XX: int = 0
REQUESTS_5XX: int = 0
REQUESTS_4XX: int = 0

# Ring buffer of the last 50 multimodal requests
LAST_REQUESTS: deque = deque(maxlen=50)
# Per-request correlation: request_id → record
REQUEST_INDEX: Dict[str, dict] = {}


def now() -> float:
    return time.time()


# ── Admin endpoints ─────────────────────────────────────────────────────────


async def healthz(request):
    return web.json_response(
        {
            "ok": True,
            "group": ARGS.group,
            "instance": ARGS.instance,
            "state": STATE["state"],
            "uptime_sec": now() - STARTED_AT,
            "requests_total": REQUESTS_TOTAL,
            "requests_2xx": REQUESTS_2XX,
            "requests_4xx": REQUESTS_4XX,
            "requests_5xx": REQUESTS_5XX,
            "last_request_count": len(LAST_REQUESTS),
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


async def admin_reset(request):
    STATE.clear()
    STATE.update(copy.deepcopy(STATE_DEFAULTS))
    STATE["state_change_at"] = now()
    STATE["quota_window_start_at"] = now()
    LAST_REQUESTS.clear()
    REQUEST_INDEX.clear()
    return web.json_response({"ok": True, "message": "reset"})


async def admin_last_requests(request):
    n = int(request.query.get("n", "10"))
    items = list(LAST_REQUESTS)[-n:]
    return web.json_response({"requests": items})


async def admin_get_request(request):
    req_id = request.query.get("id", "")
    rec = REQUEST_INDEX.get(req_id)
    if not rec:
        return web.json_response({"error": "not found", "id": req_id}, status=404)
    return web.json_response(rec)


async def admin_clear_requests(request):
    LAST_REQUESTS.clear()
    REQUEST_INDEX.clear()
    return web.json_response({"ok": True, "cleared": True})


# ── Multimodal content analysis ─────────────────────────────────────────────


def _summarize_content(content: Any) -> Dict[str, Any]:
    """Convert an OpenAI-style content field into a structural summary.

    - string  → {"type": "text", "text": <first 80 chars>}
    - list    → list of {type, ...} with size info for image/audio/file blocks
    """
    if isinstance(content, str):
        return {"type": "text", "text_preview": content[:80], "text_len": len(content)}
    if isinstance(content, list):
        out = []
        for part in content:
            if not isinstance(part, dict):
                out.append({"type": "unknown", "raw_repr": repr(part)[:80]})
                continue
            ptype = part.get("type", "unknown")
            entry: Dict[str, Any] = {"type": ptype}
            if ptype == "text":
                entry["text_preview"] = (part.get("text", "") or "")[:80]
                entry["text_len"] = len(part.get("text", "") or "")
            elif ptype == "image_url":
                iu = part.get("image_url", {})
                url = iu.get("url", "") if isinstance(iu, dict) else ""
                entry["url_kind"] = _classify_url(url)
                entry["url_len"] = len(url)
                if entry["url_kind"] == "data":
                    # data:image/png;base64,<...>
                    _, b64data = url.split(",", 1) if "," in url else ("", "")
                    entry["base64_len"] = len(b64data)
                    entry["base64_sha256"] = hashlib.sha256(
                        b64data.encode("utf-8")
                    ).hexdigest()[:16]
            elif ptype == "input_audio":
                ia = part.get("input_audio", {})
                if isinstance(ia, dict):
                    entry["audio_format"] = ia.get("format", "")
                    entry["data_len"] = len(ia.get("data", "") or "")
            elif ptype == "file":
                entry["file_id"] = part.get("file_id") or part.get("file", {}).get(
                    "file_id"
                )
            elif ptype == "image":
                # Anthropic native
                src = part.get("source", {})
                if isinstance(src, dict):
                    entry["src_type"] = src.get("type", "")
                    entry["media_type"] = src.get("media_type", "")
                    data = src.get("data", "") or ""
                    entry["data_len"] = len(data)
                    entry["data_sha256"] = hashlib.sha256(
                        data.encode("utf-8")
                    ).hexdigest()[:16]
            out.append(entry)
        return {"type": "list", "parts": out, "part_count": len(out)}
    return {"type": "other", "raw_repr": repr(content)[:80]}


def _classify_url(url: str) -> str:
    if not url:
        return "empty"
    if url.startswith("data:"):
        return "data"
    if url.startswith("https://"):
        return "https"
    if url.startswith("http://"):
        return "http"
    return "other"


def _extract_fingerprint_headers(request) -> Dict[str, Any]:
    """Capture X-Device-Seed / X-Machine-Id / X-Client-Profile / X-Runtime-Name /
    X-Runtime-Version / X-OS-Name / X-OS-Arch / User-Agent / X-Tenant-Id
    so the test client can assert identity-funneling end-to-end."""
    h = request.headers
    return {
        "X-Device-Seed": h.get("X-Device-Seed", ""),
        "X-Machine-Id": h.get("X-Machine-Id", ""),
        "X-Client-Profile": h.get("X-Client-Profile", ""),
        "X-Runtime-Name": h.get("X-Runtime-Name", ""),
        "X-Runtime-Version": h.get("X-Runtime-Version", ""),
        "X-OS-Name": h.get("X-OS-Name", ""),
        "X-OS-Arch": h.get("X-OS-Arch", ""),
        "X-Tenant-Id": h.get("X-Tenant-Id", ""),
        "User-Agent": h.get("User-Agent", ""),
        "Authorization-Redacted": _redact_auth(h.get("Authorization", "")),
    }


def _redact_auth(auth_header: str) -> str:
    if not auth_header:
        return ""
    if auth_header.lower().startswith("bearer "):
        tok = auth_header[7:]
        if len(tok) >= 8:
            return f"Bearer {tok[:4]}…{tok[-4:]}"
    return "<redacted>"


def _record_request(
    request, endpoint: str, parsed_body: Optional[dict], parse_error: Optional[str]
) -> str:
    """Store the captured request under a generated id, return the id."""
    rid = f"mm-{uuid.uuid4().hex[:16]}"
    fp_headers = _extract_fingerprint_headers(request)
    messages = parsed_body.get("messages", []) if parsed_body else []
    msg_summaries = []
    for m in messages:
        if not isinstance(m, dict):
            continue
        msg_summaries.append(
            {
                "role": m.get("role", ""),
                "name": m.get("name", ""),
                "content_summary": _summarize_content(m.get("content", "")),
            }
        )
    record = {
        "id": rid,
        "ts": now(),
        "endpoint": endpoint,
        "gateway_request_id": request.headers.get("X-Request-Id", ""),
        "model": (parsed_body or {}).get("model", ""),
        "stream": bool((parsed_body or {}).get("stream", False)),
        "max_tokens": (parsed_body or {}).get("max_tokens"),
        "temperature": (parsed_body or {}).get("temperature"),
        "messages": msg_summaries,
        "fingerprint_headers": fp_headers,
        "parse_error": parse_error,
        "raw_size": len(parsed_body.get("__raw__", b"")) if parsed_body else 0,
    }
    LAST_REQUESTS.append(record)
    REQUEST_INDEX[rid] = record
    return rid


# ── Business endpoints ─────────────────────────────────────────────────────


async def chat_completions(request):
    """OpenAI Chat Completions with multimodal content support."""
    global REQUESTS_TOTAL, REQUESTS_2XX, REQUESTS_5XX, REQUESTS_4XX
    REQUESTS_TOTAL += 1
    raw = await request.read()
    parsed: Optional[dict] = None
    parse_error: Optional[str] = None
    try:
        parsed = json.loads(raw.decode("utf-8")) if raw else {}
    except Exception as e:
        parse_error = repr(e)
        parsed = {}
    if parsed is not None:
        parsed["__raw__"] = raw  # size capture; not actually sent anywhere

    s = STATE["state"]

    # Hard failure injection
    if s == "server_error":
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "server", "message": "chaos server_error"}},
            status=500,
        )
    if s == "auth":
        REQUESTS_4XX += 1
        return web.json_response(
            {"error": {"type": "auth", "message": "chaos 401"}}, status=401
        )
    if s == "dropped":
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "dropped", "message": "mock dropped"}}, status=503
        )

    # Latency
    delay_ms = 0
    if STATE["latency_ms_extra"] > 0 and random.random() < STATE["latency_prob"]:
        delay_ms += STATE["latency_ms_extra"]
    if s == "slow":
        delay_ms += random.randint(200, 800)
    if delay_ms > 0:
        await asyncio.sleep(delay_ms / 1000.0)

    if STATE["fail_rate"] > 0 and random.random() < STATE["fail_rate"]:
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "flaky", "message": "chaos fail_rate"}},
            status=random.choice([500, 502, 503]),
        )

    rid = _record_request(request, "/v1/chat/completions", parsed, parse_error)

    model = (parsed or {}).get("model", "loadtest-default")
    msgs = (parsed or {}).get("messages", []) or []
    cid = f"mm-{uuid.uuid4().hex[:16]}"
    ts = int(time.time())

    # Build a multimodal-aware reply: count the modalities we received
    has_image = 0
    has_audio = 0
    has_file = 0
    has_text = 0
    for m in msgs:
        c = m.get("content", "")
        if isinstance(c, list):
            for part in c:
                if not isinstance(part, dict):
                    continue
                ptype = part.get("type")
                if ptype == "image_url" or ptype == "image":
                    has_image += 1
                elif ptype == "input_audio":
                    has_audio += 1
                elif ptype == "file":
                    has_file += 1
                elif ptype == "text":
                    has_text += 1
        else:
            has_text += 1

    summary = f"got_text={has_text} image={has_image} audio={has_audio} file={has_file}"

    if (parsed or {}).get("stream"):
        resp = web.StreamResponse(
            status=200,
            headers={
                "Content-Type": "text/event-stream",
                "Cache-Control": "no-cache",
                "X-Accel-Buffering": "no",
                "X-Mock-Request-Id": rid,
            },
        )
        await resp.prepare(request)
        for ch in summary:
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
                    "message": {
                        "role": "assistant",
                        "content": f"[{ARGS.group}/{ARGS.instance}] mock-mm: {summary}",
                    },
                    "finish_reason": "stop",
                    "logprobs": None,
                }
            ],
            "usage": {"prompt_tokens": 50, "completion_tokens": 20, "total_tokens": 70},
            "_mock_meta": {
                "request_id": rid,
                "request_count": len(LAST_REQUESTS),
            },
        },
        headers={"X-Mock-Request-Id": rid},
    )


async def messages(request):
    """Anthropic-style /v1/messages with multimodal content blocks."""
    global REQUESTS_TOTAL, REQUESTS_2XX, REQUESTS_5XX, REQUESTS_4XX
    REQUESTS_TOTAL += 1
    raw = await request.read()
    parsed: Optional[dict] = None
    parse_error: Optional[str] = None
    try:
        parsed = json.loads(raw.decode("utf-8")) if raw else {}
    except Exception as e:
        parse_error = repr(e)
        parsed = {}
    if parsed is not None:
        parsed["__raw__"] = raw

    s = STATE["state"]
    if s == "server_error":
        REQUESTS_5XX += 1
        return web.json_response(
            {"error": {"type": "server", "message": "chaos server_error"}}, status=500
        )

    rid = _record_request(request, "/v1/messages", parsed, parse_error)

    model = (parsed or {}).get("model", "loadtest-default")
    cid = f"mm-{uuid.uuid4().hex[:16]}"
    ts = int(time.time())
    has_image = 0
    has_audio = 0
    has_text = 0
    for m in (parsed or {}).get("messages", []):
        c = m.get("content", "")
        if isinstance(c, list):
            for part in c:
                if not isinstance(part, dict):
                    continue
                ptype = part.get("type")
                if ptype == "image":
                    has_image += 1
                elif ptype == "input_audio" or ptype == "audio":
                    has_audio += 1
                elif ptype == "text":
                    has_text += 1
        else:
            has_text += 1
    summary = f"anthropic_msg text={has_text} image={has_image} audio={has_audio}"

    REQUESTS_2XX += 1
    return web.json_response(
        {
            "id": cid,
            "type": "message",
            "role": "assistant",
            "model": model,
            "content": [
                {
                    "type": "text",
                    "text": f"[{ARGS.group}/{ARGS.instance}] mock-mm: {summary}",
                }
            ],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 50, "output_tokens": 20},
            "_mock_meta": {
                "request_id": rid,
                "request_count": len(LAST_REQUESTS),
            },
        },
        headers={"X-Mock-Request-Id": rid},
    )


async def list_models(request):
    ts = int(time.time())
    models = [
        "minimax-m3",
        "gpt-5.6-luna",
        "gpt-5.6-terra",
        "claude-sonnet-5",
        "gpt-4o",
        "loadtest-vision-alpha",
        "loadtest-vision-beta",
        "loadtest-vision-gamma",
    ]
    return web.json_response(
        {
            "object": "list",
            "data": [
                {"id": m, "object": "model", "created": ts, "owned_by": f"mock-mm"}
                for m in models
            ],
        }
    )


# ── Wiring ─────────────────────────────────────────────────────────────────


def build_app():
    app = web.Application()
    app.router.add_get("/healthz", healthz)
    app.router.add_get("/v1/models", list_models)
    app.router.add_post("/v1/chat/completions", chat_completions)
    app.router.add_post("/v1/messages", messages)
    app.router.add_get("/admin/state", admin_get_state)
    app.router.add_post("/admin/state", admin_set_state)
    app.router.add_post("/admin/profile", admin_set_profile)
    app.router.add_post("/admin/reset", admin_reset)
    app.router.add_get("/admin/requests", admin_last_requests)
    app.router.add_get("/admin/request", admin_get_request)
    app.router.add_post("/admin/requests/clear", admin_clear_requests)
    return app


def main():
    global ARGS, STARTED_AT
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default=os.getenv("MOCK_HOST", "127.0.0.1"))
    parser.add_argument("--port", type=int, required=True)
    parser.add_argument("--group", default="MM")
    parser.add_argument("--instance", type=int, default=0)
    parser.add_argument("--state", default=os.getenv("INIT_STATE", "healthy"))
    parser.add_argument("--quiet", action="store_true")
    parsed_args = parser.parse_args()
    ARGS.group = parsed_args.group
    ARGS.instance = parsed_args.instance
    ARGS.host = parsed_args.host
    ARGS.port = parsed_args.port
    ARGS.state = parsed_args.state
    ARGS.quiet = parsed_args.quiet

    STATE["state"] = ARGS.state
    STATE["state_change_at"] = now()
    STATE["quota_window_start_at"] = now()
    STARTED_AT = now()

    if not ARGS.quiet:
        print(
            f"[{ARGS.group}/{ARGS.instance}] multimodal supplier listening on "
            f"http://{ARGS.host}:{ARGS.port} state={ARGS.state}",
            file=sys.stderr,
            flush=True,
        )

    web.run_app(
        build_app(), host=ARGS.host, port=ARGS.port, access_log=None, print=None
    )


if __name__ == "__main__":
    main()
