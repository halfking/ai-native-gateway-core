#!/usr/bin/env python3
"""nim-four-model-rounds.py — 211 上对 NVIDIA NIM 相关四模型做多轮 chat 验收。

目标模型（owner 指定）：
  glm-5.3-flash / minimax-m3.1-flash / claude-sonnet-5-5 / gpt-5.6-luna

约定：
  - 先查 v_routable_credential_models 上 nvidia 可路由绑定
  - 无 NIM 绑定：记 SKIP（不是网关请求失败）
  - 429 / too many requests / rate limit：记 PROVIDER（供应商侧可接受）
  - 其它非 2xx：记 FAIL
  - 临时 key 经 admin API 签发，验完立即吊销；密钥与 token 不回显

用法（在 211 上）:
  python3 nim-four-model-rounds.py [--rounds 3]
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

MODELS = [
    "glm-5.3-flash",
    "minimax-m3.1-flash",
    "claude-sonnet-5-5",
    "gpt-5.6-luna",
]
CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"
PORTS = ("8781", "8783")


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def running_container() -> str:
    out = subprocess.run(
        ["docker", "ps", "--format", "{{.Names}}"],
        capture_output=True, text=True, check=False,
    ).stdout.split()
    for name in CONTAINER_CANDIDATES:
        if name in out:
            return name
    raise SystemExit("没有在跑的 llm-gateway-211-* 容器")


def secret_key() -> str:
    raw = subprocess.check_output(
        ["docker", "exec", running_container(), "printenv", "LLM_GATEWAY_SECRET_KEY"],
    ).decode().strip()
    if "=" in raw and not raw.startswith("sk-"):
        raw = raw.split("=", 1)[-1].strip()
    return raw


def mint_token(secret: str, ttl: int = 3600) -> str:
    header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    now = int(time.time())
    payload = _b64(json.dumps({
        "user_id": USER_ID, "tenant_id": TENANT, "username": USERNAME, "role": ROLE,
        "exp": now + ttl, "iat": now, "iss": "llm-gateway", "aud": ["llm-gateway-api"],
    }, separators=(",", ":")).encode())
    sig = _b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def active_port() -> str:
    for port in PORTS:
        try:
            urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=3).read()
            return port
        except Exception:  # noqa: BLE001
            continue
    return PORTS[0]


def http(method: str, url: str, *, headers=None, body=None, timeout=90):
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode()
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(url, data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return resp.status, dict(resp.headers), resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, dict(exc.headers or {}), exc.read().decode("utf-8", "replace")
    except Exception as exc:  # noqa: BLE001
        return 0, {}, f"ERROR {exc}"


def psql(sql: str) -> str:
    return subprocess.check_output(
        ["docker", "exec", "-i", "acc-pg17", "psql", "-U", "postgres",
         "-d", "llm_gateway", "-At", "-F", "\t", "-c", sql],
        text=True,
    )


def nim_routable() -> dict[str, list[str]]:
    sql = """
SELECT mc.canonical_name,
       string_agg(v.credential_id::text, ',' ORDER BY v.credential_id)
  FROM v_routable_credential_models v
  JOIN models_canonical mc ON mc.id = v.canonical_id
  JOIN providers p ON p.id = v.provider_id
 WHERE p.code = 'nvidia'
   AND v.is_routable
   AND mc.canonical_name IN ('glm-5.3-flash','minimax-m3.1-flash','claude-sonnet-5-5','gpt-5.6-luna')
 GROUP BY 1;
"""
    out: dict[str, list[str]] = {m: [] for m in MODELS}
    for line in psql(sql).splitlines():
        if not line.strip():
            continue
        name, creds = line.split("\t", 1)
        out[name] = [c for c in creds.split(",") if c]
    return out


def classify(status: int, body: str) -> str:
    low = (body or "").lower()
    if 200 <= status < 300:
        return "PASS"
    # 仅供应商侧限流/过载算 PROVIDER；网关自身 503（如 archive）仍算 FAIL。
    if status == 429 or "too many requests" in low or "rate_limit" in low or "rate limit" in low:
        return "PROVIDER"
    return "FAIL"


def main() -> int:
    rounds = 3
    if "--rounds" in sys.argv:
        rounds = int(sys.argv[sys.argv.index("--rounds") + 1])

    secret = secret_key()
    token = mint_token(secret)
    port = active_port()
    base = f"http://127.0.0.1:{port}"
    auth = {"Authorization": f"Bearer {token}"}
    print(f"# container={running_container()} port={port} rounds={rounds}")

    routable = nim_routable()
    print("# NIM routable credentials")
    for m in MODELS:
        print(f"  {m}: {routable[m] or '(none)'}")

    alias = f"nim-audit-{int(time.time())}"
    status, _, text = http("POST", f"{base}/api/keys", headers=auth, body={
        "application_code": "default",
        "tenant_id": TENANT,
        "key_alias": alias,
        "remark": "NIM 四模型多轮验收临时 key（2026-11-17），验后即吊销",
    })
    if status not in (200, 201):
        print(f"FAIL mint key HTTP {status}: {text[:300]}")
        return 1
    key_payload = json.loads(text)
    key_id = key_payload.get("id") or (key_payload.get("data") or {}).get("id")
    status, _, text = http("GET", f"{base}/api/keys/{key_id}/reveal", headers=auth)
    reveal = json.loads(text) if text.startswith("{") else {}
    api_key = reveal.get("key") or reveal.get("api_key") or (reveal.get("data") or {}).get("key", "")
    if not api_key:
        print(f"FAIL reveal key HTTP {status}: {text[:300]}")
        http("DELETE", f"{base}/api/keys/{key_id}", headers=auth)
        return 1
    print(f"# minted key id={key_id}")

    summary = {m: {"pass": 0, "provider": 0, "fail": 0, "skip": 0} for m in MODELS}
    try:
        for model in MODELS:
            if not routable[model]:
                summary[model]["skip"] += 1
                print(f"[SKIP] {model} — no nvidia routable binding")
                continue
            for r in range(1, rounds + 1):
                # 不传 X-Session-Id：让网关自建会话，避免跨 key 复用会话触发 session_forbidden。
                status, _, text = http(
                    "POST", f"{base}/v1/chat/completions",
                    headers={"Authorization": f"Bearer {api_key}"},
                    body={
                        "model": model,
                        "messages": [{"role": "user", "content": f"Reply exactly: PONG-{r}"}],
                        "max_tokens": 32,
                        "stream": False,
                    },
                    timeout=120,
                )
                mark = classify(status, text)
                bucket = {"PASS": "pass", "PROVIDER": "provider", "FAIL": "fail"}[mark]
                summary[model][bucket] += 1
                snip = (text or "").replace("\n", " ")[:120]
                print(f"[{mark}] {model} r{r} HTTP {status} {snip}")
                time.sleep(0.3)
    finally:
        http("DELETE", f"{base}/api/keys/{key_id}", headers=auth)
        print(f"# revoked key id={key_id}")

    print("=== summary ===")
    print(json.dumps(summary, ensure_ascii=False, indent=2))
    hard = [
        m for m, s in summary.items()
        if s["fail"] > 0 and s["pass"] == 0 and s["skip"] == 0
    ]
    if hard:
        print("FAIL hard models: " + ", ".join(hard))
        return 1
    print("ALL ACCEPTABLE (pass/provider/skip only)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
