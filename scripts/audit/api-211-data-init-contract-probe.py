#!/usr/bin/env python3
"""api-211-data-init-contract-probe.py —— 在 211 上验「数据初始化」三端点的**线上契约**。

用法（在 211 上执行）:
  python3 api-211-data-init-contract-probe.py
输出: 一行 JSON（{"checks": [...], "summary": {...}}）

用途：2026-11-17 「execute 真删行 + 每类别归属」轮的线上验收。

★ 三条纪律：
  1. **密钥与 token 不出机器**：签名在 211 本机完成（用容器里的
     LLM_GATEWAY_SECRET_KEY），本脚本只把 HTTP 状态码与响应体的**摘要**带回来。
  2. **不发一次真正的删除**：本探针只打**拒绝路径**（缺令牌 / 篡改令牌）
     ⇒ 它**不可能**删掉任何一行生产数据；「行数未变」由调用方另用 psql 对拍。
  3. **端口自发现**：蓝绿部署后活跃槽可能是 8781 也可能是 8783
     ⇒ 逐个探 /readyz，取第一个 ready 的。
"""
import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.error
import urllib.request

CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
PORTS = ("8781", "8783")
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"


def _running_container() -> str:
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


def mint_token(ttl: int = 1800) -> str:
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


def pick_port() -> str:
    for port in PORTS:
        try:
            with urllib.request.urlopen(f"http://127.0.0.1:{port}/readyz", timeout=8) as resp:
                body = resp.read().decode("utf-8", "replace")
                if resp.status == 200 and '"status":"ready"' in body.replace(" ", ""):
                    return port
        except Exception:  # noqa: BLE001
            continue
    raise SystemExit("两个槽都没 ready")


def call(port: str, token: str, method: str, path: str, body: dict | None = None):
    data = None
    headers = {"Authorization": f"Bearer {token}"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}", data=data, headers=headers, method=method
    )
    try:
        with urllib.request.urlopen(req, timeout=90) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", "replace")
    except Exception as exc:  # noqa: BLE001
        return 0, str(exc)


def main() -> None:
    port = pick_port()
    token = mint_token()
    checks: list[dict] = []

    def record(name: str, ok: bool, detail: str = "") -> None:
        checks.append({"name": name, "ok": bool(ok), "detail": detail})

    base = "/api/admin/data-lifecycle/data-init"

    # ① catalog：9 类、缺表清单。
    code, body = call(port, token, "GET", f"{base}/catalog")
    cats, missing = [], []
    if code == 200:
        try:
            doc = json.loads(body)
            cats = doc.get("categories") or []
            missing = {c.get("key"): (c.get("missing_tables") or []) for c in cats if c.get("missing_tables")}
        except Exception:  # noqa: BLE001
            pass
    record("catalog_200", code == 200, f"HTTP={code} 类别数={len(cats)}")
    record("catalog_9_categories", len(cats) == 9, f"类别数={len(cats)}")
    record("catalog_no_missing_tables", not missing, f"缺表={missing}")

    # ② preview：签发令牌 + 目标行数。
    code, body = call(port, token, "POST", f"{base}/preview", {"categories": ["request_records"]})
    token_issued, target_rows = False, -1
    if code == 200:
        try:
            doc = json.loads(body)
            token_issued = bool(doc.get("confirm_token"))
            target_rows = int(doc.get("total_target_rows", -1))
        except Exception:  # noqa: BLE001
            pass
    record("preview_200", code == 200, f"HTTP={code}")
    record("preview_issues_token", token_issued, f"签发={token_issued}")
    record("preview_target_rows_positive", target_rows > 0, f"total_target_rows={target_rows}")

    # ③ execute 缺令牌 ⇒ 400（**不删任何行**）。
    code, body = call(port, token, "POST", f"{base}/execute", {"categories": ["request_records"]})
    record("execute_missing_token_400", code == 400, f"HTTP={code} body={body[:120]}")
    record("execute_error_body_no_sql_leak", "DELETE" not in body, "响应体不含 SQL 片段")

    # ④ execute 篡改令牌 ⇒ 400（**不删任何行**）。
    raw, mac = ("00" + "00" * 20), ("00" + "00" * 20)
    code, body = call(port, token, "POST", f"{base}/execute",
                      {"categories": ["request_records"], "confirm_token": f"{raw}.{mac}"})
    record("execute_tampered_token_400", code == 400, f"HTTP={code} body={body[:120]}")

    # ⑤ execute 缺类别 ⇒ 400。
    code, body = call(port, token, "POST", f"{base}/execute", {"confirm_token": f"{raw}.{mac}"})
    record("execute_missing_categories_400", code == 400, f"HTTP={code}")

    summary = {
        "port": port,
        "categories": len(cats),
        "missing_tables": missing,
        "preview_target_rows": target_rows,
        "failed": sum(1 for c in checks if not c["ok"]),
        "total": len(checks),
    }
    print(json.dumps({"checks": checks, "summary": summary}, ensure_ascii=False))


if __name__ == "__main__":
    main()
