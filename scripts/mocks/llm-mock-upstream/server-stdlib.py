#!/usr/bin/env python3
"""Mock LLM upstream —— **零依赖**（只用 Python 标准库）版。

Run: python3 server-stdlib.py
Env: MOCK_PORT=8901 MOCK_BIND=127.0.0.1 MOCK_TOKEN=mock-token-8901
     MOCK_MODELS="a,b,c" MOCK_LATENCY_MS=60

★ MOCK_BIND=0.0.0.0 的用途：网关跑在**容器**里时，provider 的 base_url
  若写 127.0.0.1 ⇒ 指的是**容器自己** ⇒ 连不上宿主机上的 mock ⇒
  必须用 host.docker.internal（Docker Desktop）⇒ mock 得绑到 0.0.0.0 才可达。

# 为什么要有这个文件（2026-10-21 R72b 轮实测）

仓内已有两个 mock 上游，各有各的缺口：

| 实现 | 依赖 | 缺口 |
| --- | --- | --- |
| `server-v3.py`（Python） | **aiohttp**（本机没装 ⇒ 起不来） | — |
| `tests/stress/mocks`（Go） | 无 | **不提供 `/v1/models`** |
| `tests/local/mocks`（Go） | 无 | 不提供 `/v1/models`，且响应体固定 |

而网关的凭据健康探针是**两步法**（`bg/credential_probe_v2.go`：
`probeCredentialWithCapability`）：

    Step 1: GET /v1/models      ← 目录探测
    Step 2: 一条 mini chat "hi"  ← 连通性探测

⇒ **mock 不提供 `/v1/models` ⇒ 探针 Step 1 失败 ⇒ 凭据被判
`health_status='unreachable'` ⇒ 被路由候选过滤掉**
（`domains/streaming/models.go`：候选必须满足
`COALESCE(c.health_status,'unknown') IN ('healthy','unknown')`）
⇒ 症状是「mock 明明在跑、却一个请求都收不到」
（本轮实测：mock 的计数器 requests=0）。

⇒ 所以本文件只做一件事：**把 `/v1/models` 补上**，并让 chat 响应回显
请求里的模型名（这样落库的 `raw_model_name` 与请求一致，便于核对）。
零依赖 ⇒ 不需要装任何东西（本机 python3 即可跑）。

# 接口

    GET  /v1/models          → OpenAI 兼容模型目录
    POST /v1/chat/completions → OpenAI 兼容对话补全（回显 model，支持 stream）
    GET  /healthz            → {"status":"ok"}
    GET  /admin/stats        → 计数器

# 安全边界

它是**测试夹具**：不校验 token、不做配额、不记日志到磁盘。
只应监听回环地址。
"""

import json
import os
import random
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = int(os.environ.get("MOCK_PORT", "8901"))
BIND = os.environ.get("MOCK_BIND", "127.0.0.1")
TOKEN = os.environ.get("MOCK_TOKEN", "mock-token-%d" % PORT)
LATENCY_MS = int(os.environ.get("MOCK_LATENCY_MS", "60"))
MODELS = [m for m in os.environ.get(
    "MOCK_MODELS",
    "glm-5.3-flash,glm-5.2,minimax-m2.7,deepseek-v4-flash,mimo-v2.6-flash,"
    "grok-4.6,claude-sonnet-5-5,gpt-5.6-luna",
).split(",") if m.strip()]

COUNTERS = {"requests": 0, "success": 0, "client_error": 0, "stream_chunks": 0}
LOCK = threading.Lock()


def bump(key, n=1):
    with LOCK:
        COUNTERS[key] += n


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):  # 静默：避免刷屏
        pass

    def _json(self, code, payload):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    # ── GET ─────────────────────────────────────────────────────────────
    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path in ("/v1/models", "/models"):
            self._json(200, {
                "object": "list",
                "data": [{"id": m, "object": "model", "created": int(time.time()),
                          "owned_by": "mock-stdlib"} for m in MODELS],
            })
            return
        if path == "/healthz":
            self._json(200, {"status": "ok", "port": PORT})
            return
        if path == "/admin/stats":
            with LOCK:
                snap = dict(COUNTERS)
            self._json(200, snap)
            return
        self._json(404, {"error": {"message": "not found", "type": "invalid_request_error"}})

    # ── POST ────────────────────────────────────────────────────────────
    def do_POST(self):
        path = self.path.split("?", 1)[0]
        if path not in ("/v1/chat/completions", "/chat/completions"):
            self._json(404, {"error": {"message": "not found", "type": "invalid_request_error"}})
            return
        bump("requests")
        try:
            length = int(self.headers.get("Content-Length") or 0)
            req = json.loads(self.rfile.read(length) or b"{}")
        except Exception:
            bump("client_error")
            self._json(400, {"error": {"message": "bad json", "type": "invalid_request_error"}})
            return

        model = req.get("model") or (MODELS[0] if MODELS else "mock-model")
        messages = req.get("messages") or []
        prompt = ""
        for m in reversed(messages):
            if m.get("role") == "user":
                prompt = str(m.get("content") or "")
                break
        if LATENCY_MS > 0:
            time.sleep(random.uniform(LATENCY_MS * 0.5, LATENCY_MS * 1.5) / 1000.0)

        # 回显：内容里带上提示词前 40 字符 + 模型名 ⇒ 落库与响应对得上
        reply = "mock(%s): %s" % (model, prompt[:40])
        prompt_tokens = max(1, len(prompt) // 4)
        completion_tokens = max(1, len(reply) // 4)

        if req.get("stream"):
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            for i in range(3):
                chunk = {"id": "chatcmpl-mock", "object": "chat.completion.chunk",
                         "created": int(time.time()), "model": model,
                         "choices": [{"index": 0, "delta": {"content": reply[:len(reply) // 3 * (i + 1)][len(reply) // 3 * i:]},
                                      "finish_reason": None}]}
                self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode("utf-8"))
                self.wfile.flush()
                bump("stream_chunks")
                time.sleep(0.02)
            done = {"id": "chatcmpl-mock", "object": "chat.completion.chunk",
                    "created": int(time.time()), "model": model,
                    "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
                    "usage": {"prompt_tokens": prompt_tokens,
                              "completion_tokens": completion_tokens,
                              "total_tokens": prompt_tokens + completion_tokens}}
            self.wfile.write(("data: " + json.dumps(done) + "\n\n").encode("utf-8"))
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()
            bump("success")
            return

        bump("success")
        self._json(200, {
            "id": "chatcmpl-mock-%d" % int(time.time() * 1000),
            "object": "chat.completion",
            "created": int(time.time()),
            "model": model,
            "choices": [{"index": 0, "message": {"role": "assistant", "content": reply},
                         "finish_reason": "stop"}],
            "usage": {"prompt_tokens": prompt_tokens,
                      "completion_tokens": completion_tokens,
                      "total_tokens": prompt_tokens + completion_tokens},
        })


def main():
    srv = ThreadingHTTPServer((BIND, PORT), Handler)
    print("mock-stdlib listening on %s:%d (models=%s)" % (BIND, PORT, ",".join(MODELS)), flush=True)
    try:
        srv.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        srv.server_close()


if __name__ == "__main__":
    main()
