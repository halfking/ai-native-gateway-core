#!/usr/bin/env python3
"""mint-jwt-token.py —— 从 211 容器读 LLM_GATEWAY_SECRET_KEY，签一枚短期 super_admin JWT。

用法（在 211 上执行）:
  python3 mint-jwt-token.py [--ttl 3600] [--output /tmp/token.txt]

输出:
  · 默认：JWT 写到 stdout（单行）
  · --output <file>：JWT 写进文件（0600 权限）

用途：
  · maestro/flows/21-users-page-role-governance.yaml 的 CDP 预置会话
  · maestro/tools/seed-session.mjs 的 --token-file 参数

安全约束：
  · 密钥与 token **不落盘、不打印**（除非显式用 --output 指定路径）
  · 仅在 211 本机可用（需 docker exec 权限访问容器环境变量）
"""
import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import os

CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"


def _running_container() -> str:
    """蓝绿部署后活跃槽可能是 a 也可能是 b ⇒ 取第一个在跑的。"""
    out = subprocess.run(
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
    ttl = 3600
    output_file = None
    i = 1
    while i < len(sys.argv):
        if sys.argv[i] == "--ttl":
            ttl = int(sys.argv[i + 1])
            i += 2
        elif sys.argv[i] == "--output":
            output_file = sys.argv[i + 1]
            i += 2
        else:
            print(f"usage: mint-jwt-token.py [--ttl 3600] [--output /path/to/token.txt]", file=sys.stderr)
            sys.exit(2)
    
    token = mint_token(ttl)
    
    if output_file:
        # 0600 权限：只有属主可读写
        with open(output_file, "w", opener=lambda path, flags: os.open(path, flags, 0o600)) as f:
            f.write(token)
        print(f"JWT written to {output_file}", file=sys.stderr)
    else:
        # 默认：写到 stdout（单行）
        print(token)


if __name__ == "__main__":
    main()
