#!/usr/bin/env python3
# docs/全方面测试/tools/chat_rounds_client.py
#
# Multi-round chat client with **accumulating messages** semantics.
#
# Each round appends a user message to a running `messages` list, POSTs
# the full list, then appends the assistant's reply. This mimics how a
# real chat UI (Cursor, ChatGPT web, etc.) maintains client-side history
# and lets the gateway-side session cache / compression observe the
# growing token/count footprint.
#
# Used by:
#   - S20 (auto-title): 1 round, verify session_titles
#   - S22 (instant summary): 5-10 rounds, verify session_summaries
#   - S23 (long-text chunked): 30-80 rounds, verify request_logs.compression_*
#
# Design references:
#   - docs/全方面测试/17-会话压缩与缓存测试方案.md §9.1 (compression_test_client.py draft)
#   - docs/全方面测试/00-总览.md (test tool design)
#
# Usage:
#   python3 chat_rounds_client.py \
#     --session-id s20-$(uuidgen) \
#     --rounds 5 \
#     --model loadtest-mini-alpha \
#     --content-template "Round {i} of test session" \
#     --gateway http://127.0.0.1:8793 \
#     --api-key sk-loadtest-01 \
#     --output /tmp/chat_rounds.json
#
# Output JSON shape:
#   {
#     "session_id": "...",
#     "model": "...",
#     "rounds_total": N,
#     "rounds_succ": M,
#     "rounds_fail": K,
#     "by_round": [{"round": 1, "status": 200, "ms": 250, "msg_count": 2}, ...],
#     "messages_final_count": 2*N,
#     "elapsed_sec": 12.3
#   }
#
# 2026-08-06: created for S20-S23.

import argparse
import asyncio
import json
import sys
import time
import uuid
from typing import Any, Dict, List, Optional

import aiohttp


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Multi-round accumulating chat client (S20-S23 test driver)."
    )
    p.add_argument("--gateway", required=True, help="Gateway base URL, e.g. http://127.0.0.1:8793")
    p.add_argument("--api-key", required=True, help="Bearer token (sk-loadtest-NN or admin key)")
    p.add_argument("--session-id", required=True, help="X-Gw-Session-Id value (must be stable across rounds)")
    p.add_argument("--model", default="loadtest-mini-alpha", help="Model name in messages")
    p.add_argument(
        "--rounds", type=int, default=5, help="Number of chat rounds (each round = 1 user + 1 assistant)"
    )
    p.add_argument(
        "--content-template",
        default="Round {i} question: please describe step {i} of the workflow.",
        help="Template for the user message at round i (use {i} for round number).",
    )
    p.add_argument(
        "--prompt",
        default=None,
        help="Shortcut preset: 'short' (~50 chars), 'medium' (~500 chars), 'long' (~3000 chars). Overrides --content-template.",
    )
    p.add_argument(
        "--assistant-template",
        default="Round {i} answer: step {i} explanation here.",
        help="Template for the assistant message appended after a 200 response (use {i} for round).",
    )
    p.add_argument(
        "--messages-strategy",
        choices=["accumulate", "fixed-1"],
        default="accumulate",
        help="accumulate: each round sends full history; fixed-1: each round sends only 1 user msg (no history).",
    )
    p.add_argument("--stream", action="store_true", help="Send stream=true (SSE)")
    p.add_argument(
        "--no-cache", action="store_true", help="Send X-Gw-No-Cache: true (bypass L1/L2 cache)"
    )
    p.add_argument(
        "--x-gw-work-type", default="", help="Optional X-Gw-Work-Type header (e.g. session_title)"
    )
    p.add_argument(
        "--rps", type=float, default=0, help="Per-round sleep (1/rps); 0 = back-to-back"
    )
    p.add_argument(
        "--timeout", type=float, default=30.0, help="Per-request timeout in seconds"
    )
    p.add_argument("--output", default="", help="Optional path to write JSON summary")
    p.add_argument(
        "--fail-fast", action="store_true",
        help="Stop on first non-2xx response (default: continue)",
    )
    return p.parse_args()


async def one_round(
    session: aiohttp.ClientSession,
    args: argparse.Namespace,
    round_idx: int,
    messages: List[Dict[str, Any]],
) -> Dict[str, Any]:
    """Send one round of chat completion. Returns a result dict."""
    user_content = args.content_template.format(i=round_idx)
    msgs_to_send: List[Dict[str, Any]]
    if args.messages_strategy == "fixed-1":
        msgs_to_send = [{"role": "user", "content": user_content}]
    else:
        # accumulate: append to client history
        messages.append({"role": "user", "content": user_content})
        msgs_to_send = list(messages)

    body = {
        "model": args.model,
        "messages": msgs_to_send,
        "max_tokens": 60,
    }
    if args.stream:
        body["stream"] = True

    headers = {
        "Authorization": f"Bearer {args.api_key}",
        "Content-Type": "application/json",
        "X-Gw-Session-Id": args.session_id,
    }
    if args.no_cache:
        headers["X-Gw-No-Cache"] = "true"
    if args.x_gw_work_type:
        headers["X-Gw-Work-Type"] = args.x_gw_work_type

    url = f"{args.gateway.rstrip('/')}/v1/chat/completions"
    t0 = time.time()
    status = 0
    assistant_content = ""
    err = ""
    try:
        async with session.post(url, json=body, headers=headers, timeout=aiohttp.ClientTimeout(total=args.timeout)) as resp:
            status = resp.status
            if status == 200:
                if args.stream:
                    # Skip parsing SSE chunks; just count tokens received
                    text = await resp.text()
                    assistant_content = f"<stream {len(text)} bytes>"
                else:
                    data = await resp.json()
                    choices = data.get("choices", [])
                    if choices:
                        msg = choices[0].get("message", {})
                        assistant_content = str(msg.get("content", ""))
            else:
                err = (await resp.text())[:200]
    except Exception as e:
        err = f"{type(e).__name__}: {e}"
    dur_ms = int((time.time() - t0) * 1000)

    # On success, append assistant reply to client history (accumulate mode)
    if status == 200 and args.messages_strategy == "accumulate" and assistant_content:
        messages.append({"role": "assistant", "content": assistant_content})

    return {
        "round": round_idx,
        "status": status,
        "ms": dur_ms,
        "msg_count": len(msgs_to_send),
        "assistant_len": len(assistant_content),
        "err": err,
    }


async def run(args: argparse.Namespace) -> Dict[str, Any]:
    messages: List[Dict[str, Any]] = []
    by_round: List[Dict[str, Any]] = []
    succ = 0
    fail = 0
    t_start = time.time()

    async with aiohttp.ClientSession() as session:
        for i in range(1, args.rounds + 1):
            res = await one_round(session, args, i, messages)
            by_round.append(res)
            if 200 <= res["status"] < 300:
                succ += 1
            else:
                fail += 1
                if args.fail_fast:
                    break
            if args.rps > 0:
                await asyncio.sleep(1.0 / args.rps)

    elapsed = time.time() - t_start
    summary = {
        "scenario": "chat_rounds",
        "session_id": args.session_id,
        "model": args.model,
        "rounds_total": args.rounds,
        "rounds_succ": succ,
        "rounds_fail": fail,
        "by_round": by_round,
        "messages_final_count": len(messages),
        "elapsed_sec": round(elapsed, 3),
        "messages_strategy": args.messages_strategy,
    }
    if args.output:
        with open(args.output, "w", encoding="utf-8") as f:
            json.dump(summary, f, ensure_ascii=False, indent=2)
    return summary


def main() -> int:
    args = parse_args()
    if not args.session_id:
        args.session_id = f"chat-rounds-{uuid.uuid4().hex[:8]}"
    # Apply --prompt preset
    if args.prompt:
        if args.prompt == "short":
            args.content_template = "Round {i}: " + ("x" * 50)
        elif args.prompt == "medium":
            args.content_template = "Round {i}: " + ("x" * 500)
        elif args.prompt == "long":
            args.content_template = "Round {i}: " + ("x" * 3000)
        else:
            print(f"unknown --prompt preset '{args.prompt}', ignoring", file=sys.stderr)
    summary = asyncio.run(run(args))
    # stdout: single-line summary for shell parsing
    print(
        f"chat_rounds: total={summary['rounds_total']} succ={summary['rounds_succ']} "
        f"fail={summary['rounds_fail']} msgs_final={summary['messages_final_count']} "
        f"elapsed={summary['elapsed_sec']}s sid={summary['session_id']}"
    )
    return 0 if summary["rounds_fail"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
