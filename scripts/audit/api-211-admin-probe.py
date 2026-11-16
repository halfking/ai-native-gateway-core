#!/usr/bin/env python3
"""api-211-admin-probe.py —— 在 211 上用容器内的 LLM_GATEWAY_SECRET_KEY 现签一枚
短期 super_admin JWT，并请求网关 API。

密钥与 token 都不落盘、不回显：签名在 211 本机完成，只把 HTTP 响应体带回来。

用法（在 211 上执行）:
  python3 api-211-admin-probe.py <path-with-query> [--port 8783]
输出: HTTP 状态码 + 响应体

用途：2026-11-16 对账页数据完整性审计轮（docs/audit/2026-11-16-对账页供应商与模型
数据完整性审计-主档.md）的取证工具——/api/admin/report-rollup/{summary,dimensions}
与 /api/routing/available-models 的线上对拍。
"""
import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.request

CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"


def _running_container() -> str:
    """蓝绿部署后活跃槽可能是 a 也可能是 b ⇒ 取第一个在跑的。"""
    import subprocess as _sp

    out = _sp.run(
        ["docker", "ps", "--format", "{{.Names}}"],
        capture_output=True, text=True, check=False,
    ).stdout.split()
    for name in CONTAINER_CANDIDATES:
        if name in out:
            return name
    raise SystemExit("没有在跑的 llm-gateway-211-* 容器")


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def mint_token(ttl: int = 3600) -> str:
    container = _running_container()
    secret = subprocess.check_output(
        ["docker", "exec", container, "printenv", "LLM_GATEWAY_SECRET_KEY"]
    ).decode().strip()
    if not secret:
        raise SystemExit("LLM_GATEWAY_SECRET_KEY 为空")
    header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    now = int(time.time())
    payload = _b64(json.dumps({
        "user_id": USER_ID, "tenant_id": TENANT, "username": USERNAME, "role": ROLE,
        "exp": now + ttl, "iat": now, "iss": "llm-gateway", "aud": ["llm-gateway-api"],
    }, separators=(",", ":")).encode())
    sig = _b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def main() -> None:
    path = sys.argv[1]
    port = "8783"
    if "--port" in sys.argv:
        port = sys.argv[sys.argv.index("--port") + 1]
    token = mint_token()
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}",
        headers={"Authorization": f"Bearer {token}"},
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            body = resp.read().decode("utf-8", "replace")
            print(f"HTTP {resp.status}")
            print(body)
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", "replace")
        print(f"HTTP {exc.code}")
        print(body)
    except Exception as exc:  # noqa: BLE001
        print(f"ERROR {exc}")
        sys.exit(1)


if __name__ == "__main__":
    main()
