#!/usr/bin/env python3
"""s09_drill.py — S09 broken_stream 静默截断逐请求下钻（2026-11-15 P8 收敛轮）。

背景：S09 场景套件报 silent_truncations=8（8 笔响应既无 [DONE] 也无
upstream_incomplete 信封）——本脚本逐请求复刻该场景并输出每一笔的分类
与尾部字节，供与网关日志（request_id/§11.6 WARN/failover 痕迹）对拍。

用法（mock 栈 clients 容器内，GATEWAY_URL 可用 env 覆盖）：
    python3 s09_drill.py [N]        # N=请求数，默认 40

输出：每行一个 JSON（i/status/class/chunks/dur/tail），class ∈
{done, envelope, silent, http<code>, exc}。
"""
from __future__ import annotations

import http.client
import json
import os
import sys
import time
import uuid

GW_HOST = os.environ.get("GATEWAY_HOST", "gateway")
GW_PORT = int(os.environ.get("GATEWAY_PORT", "8781"))
API_KEY = os.environ.get("GATEWAY_API_KEY", "mock-only-gateway-key")
VENDORS = ["mock-alpha", "mock-beta", "mock-gamma", "mock-delta"]
VENDOR_PORT = 18101
MODEL = os.environ.get("DRILL_MODEL", "mock-model-fast")


def set_mode(vendor: str, mode: str) -> None:
    conn = http.client.HTTPConnection(vendor, VENDOR_PORT, timeout=5)
    conn.request(
        "POST",
        "/admin/state",
        body=json.dumps({"mode": mode}),
        headers={"Content-Type": "application/json"},
    )
    conn.getresponse().read()
    conn.close()


def one(i: int) -> None:
    sess = f"s09drill-{uuid.uuid4().hex[:10]}"
    t0 = time.time()
    try:
        conn = http.client.HTTPConnection(GW_HOST, GW_PORT, timeout=30)
        body = json.dumps(
            {
                "model": MODEL,
                "messages": [{"role": "user", "content": "drill"}],
                "max_tokens": 32,
                "stream": True,
            }
        ).encode()
        conn.request(
            "POST",
            "/v1/chat/completions",
            body=body,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {API_KEY}",
                "X-Session-Id": sess,
                "X-Gw-Session-Id": sess,
            },
        )
        resp = conn.getresponse()
        status = resp.status
        saw_done = saw_env = False
        chunks = 0
        tail = b""
        for line in resp:
            chunks += 1
            if b"[DONE]" in line:
                saw_done = True
            if b"upstream_incomplete" in line or b"eof_without_done" in line:
                saw_env = True
            tail = (tail + line)[-200:]
        conn.close()
    except Exception as exc:  # noqa: BLE001 — 下钻工具逐类记录
        print(
            json.dumps(
                {
                    "i": i,
                    "t0": round(t0, 3),
                    "class": "exc",
                    "err": f"{type(exc).__name__}: {exc}",
                },
                ensure_ascii=False,
            ),
            flush=True,
        )
        return
    if saw_done:
        cls = "done"
    elif saw_env:
        cls = "envelope"
    elif status == 200:
        cls = "silent"
    else:
        cls = f"http{status}"
    print(
        json.dumps(
            {
                "i": i,
                "t0": round(t0, 3),
                "status": status,
                "class": cls,
                "chunks": chunks,
                "dur": round(time.time() - t0, 3),
                "tail": tail.decode(errors="replace")[:160],
            },
            ensure_ascii=False,
        ),
        flush=True,
    )


def main() -> int:
    n = int(sys.argv[1]) if len(sys.argv) > 1 else 40
    for v in VENDORS:
        set_mode(v, "healthy")
    time.sleep(0.5)
    set_mode("mock-alpha", "broken_stream")
    set_mode("mock-beta", "broken_stream")
    time.sleep(0.5)
    for i in range(n):
        one(i)
    return 0


if __name__ == "__main__":
    sys.exit(main())
