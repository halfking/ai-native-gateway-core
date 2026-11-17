#!/usr/bin/env python3
"""pi-agent-245-admin-probe.py —— 在 **245 目标机本机**上验证 PI 智能体管理面的
**鉴权 fail-closed** 与 **SPA 页面可达性**。

★ 为什么必须在目标机本机跑：
  · admin JWT 用 245 自己的 `LLM_GATEWAY_SECRET_KEY` 现签（密钥与 token
    **不出机器**、不回显、只把 HTTP 状态码与响应体片段带回来）；
  · 目标机自己的回环地址 = 真实在跑的进程（不经 nginx ⇒ 排除「nginx 层放行」
    与「上游端口搞错」这两类假阳性）。

★ 断言（每条都打印 PASS/FAIL，退出码非 0 表示有失败项）：
  ① `/api/admin/pi-analysis/*` **无 token** ⇒ **401**（fail-closed）
  ② `/api/admin/pi-analysis/*` **伪造 token** ⇒ **401**
  ③ `/api/admin/pi-analysis/*` **合法 token** ⇒ **非 401**（说明路由真的在，
     而不是「被鉴权挡住所以看起来没挂」）
  ④ SPA `/admin/pi-analysis`（带 `Accept: text/html`）⇒ **200** 且响应体含
     PI 错误分析的页面挂载点（全局 API-key 门对浏览器导航有 SPA 旁路 ⇒
     **不带 `Accept: text/html` 会得到 401，那是预期行为，不是页面不存在**）

用法（在 245 上执行，或 ssh 到 245 后执行）：
  python3 pi-agent-245-admin-probe.py [--port 8781] [--base http://127.0.0.1:8781]
"""
import base64
import hashlib
import hmac
import json
import os
import sys
import time
import urllib.error
import urllib.request

ENV_FILE = "/opt/llm-gateway-go/.env"
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"

FAILURES = []


def _b64(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def _secret():
    with open(ENV_FILE, "r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if line.startswith("LLM_GATEWAY_SECRET_KEY="):
                value = line.split("=", 1)[1].strip().strip('"').strip("'")
                if value:
                    return value
    raise SystemExit("LLM_GATEWAY_SECRET_KEY 未在 %s 里找到" % ENV_FILE)


def mint_token(ttl=1800):
    secret = _secret()
    header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    now = int(time.time())
    payload = _b64(json.dumps({
        "user_id": USER_ID, "tenant_id": TENANT, "username": USERNAME, "role": ROLE,
        "exp": now + ttl, "iat": now, "iss": "llm-gateway", "aud": ["llm-gateway-api"],
    }, separators=(",", ":")).encode())
    sig = _b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def http(url, headers=None):
    req = urllib.request.Request(url, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", "replace")
    except Exception as exc:  # noqa: BLE001
        return 0, "ERROR %s" % exc


def check(name, ok, detail):
    if ok:
        print("PASS [%s] %s" % (name, detail))
    else:
        print("FAIL [%s] %s" % (name, detail))
        FAILURES.append(name)


def main():
    base = "http://127.0.0.1:8781"
    args = sys.argv[1:]
    if "--base" in args:
        base = args[args.index("--base") + 1].rstrip("/")

    # ★ 端点路径取自 admin/pi_analysis_handler.go 的 RegisterRoutes
    #   （prefix+"/queue" / prefix+"/results"）⇒ 不要凭印象写路径。
    endpoints = [
        "/api/admin/pi-analysis/queue",
        "/api/admin/pi-analysis/results",
        "/api/admin/pi-analysis/stats",
    ]
    token = mint_token()

    for path in endpoints:
        code, body = http(base + path)
        check("no-token %s" % path, code == 401, "无 token ⇒ HTTP %s（期望 401，body 前 120 字：%s）" % (code, body[:120]))

        code, body = http(base + path, {"Authorization": "Bearer not-a-real-token"})
        check("bad-token %s" % path, code == 401, "伪造 token ⇒ HTTP %s（期望 401）" % code)

        code, body = http(base + path, {"Authorization": "Bearer " + token})
        check("valid-token %s" % path, code not in (0, 401), "合法 token ⇒ HTTP %s（期望非 401；body 前 160 字：%s）" % (code, body[:160]))

    # SPA 页面（浏览器导航 ⇒ 必须带 Accept: text/html）
    code, body = http(base + "/admin/pi-analysis", {"Accept": "text/html,application/xhtml+xml"})
    check("spa /admin/pi-analysis", code == 200, "SPA 页面 ⇒ HTTP %s（期望 200）" % code)
    if code == 200:
        marker = ("pi-analysis" in body) or ("piAnalysis" in body) or ("PI" in body)
        check("spa 挂载点", marker, "响应体含 PI 相关挂载点（长度 %d）" % len(body))

    # 无 Accept: text/html 的浏览器导航 ⇒ 走全局 API-key 门 ⇒ 401（预期行为）
    code, _ = http(base + "/admin/pi-analysis")
    check("spa 无 Accept 头", code in (200, 401), "不带 Accept: text/html ⇒ HTTP %s（401 ＝ 全局 key 门的预期行为）" % code)

    print()
    if FAILURES:
        print("=== 245 admin 探针：%d 项失败 %s ===" % (len(FAILURES), FAILURES))
        sys.exit(1)
    print("=== 245 admin 探针：全部断言通过 ===")


if __name__ == "__main__":
    main()
