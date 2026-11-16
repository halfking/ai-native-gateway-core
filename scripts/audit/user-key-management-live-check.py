#!/usr/bin/env python3
"""user-key-management-live-check.py —— 「用户详情 → API 密钥管理」线上验收。

审计轮：docs/audit/2026-11-17-用户详情API密钥管理-审计轮.md
用途：部署后**在目标机上**跑一遍真实后端，证明本轮修复的行为在役：

  A. `GET /api/keys?owner=<username>` —— 服务端 owner 过滤生效（且与真库计数逐条相等）
  B. `GET /api/keys/{id}/reveal`      —— 「查看完整密钥」可用（**明文不回显**，只报长度与摘要）
  C. `POST /api/keys/{id}/enable`     —— 对 **已吊销** 密钥点「启用」必须 409（不再静默 200）
  D. C 之后复查该密钥 status/enabled  —— 确认 409 分支**没有写库**（无副作用）

密钥与 token 都不落盘、不回显：签名在目标机本机完成，响应体中的明文密钥
只取长度与 sha256 前 12 位（可对拍、不可复原）。

用法（在目标机上执行）:
  python3 user-key-management-live-check.py --port 8783
  python3 user-key-management-live-check.py --port 8783 --owners admin,e2e

退出码: 0 = 全部断言通过；1 = 有断言失败（逐条打印）。
"""
import argparse
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
USER_ID = 41
USERNAME = "admin"
TENANT = "default"
ROLE = "super_admin"

_results: list[tuple[bool, str]] = []


def _check(ok: bool, label: str) -> bool:
    _results.append((ok, label))
    print(("  PASS  " if ok else "  FAIL  ") + label)
    return ok


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


def _request(token: str, method: str, path: str, port: str, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        f"http://127.0.0.1:{port}{path}", data=data, method=method,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", "replace")


def _pg(sql: str) -> list[tuple]:
    """只读查真库（走目标机上的 PG 容器）。"""
    pg = subprocess.run(
        ["docker", "ps", "--format", "{{.Names}}"], capture_output=True, text=True, check=False,
    ).stdout.split()
    name = next((n for n in pg if n.startswith("acc-pg17") or n.startswith("acc-pg")), None)
    if name is None:
        return []
    out = subprocess.run(
        ["docker", "exec", name, "psql", "-U", "acc_super", "-d", "llm_gateway", "-t", "-A", "-F", "|", "-c", sql],
        capture_output=True, text=True, check=False,
    ).stdout
    rows = []
    for line in out.strip().splitlines():
        if line.strip():
            rows.append(tuple(line.split("|")))
    return rows


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", default="8783")
    ap.add_argument("--owners", default="admin,e2e")
    args = ap.parse_args()

    token = mint_token()
    port = args.port

    print("== A. owner 服务端过滤（并与真库计数对拍） ==")
    for owner in [o for o in args.owners.split(",") if o]:
        status, body = _request(token, "GET", f"/api/keys?owner={owner}", port)
        if not _check(status == 200, f"GET /api/keys?owner={owner} → 200 (实际 {status})"):
            continue
        keys = json.loads(body)
        db_rows = _pg(
            "SELECT count(*) FROM api_keys ak JOIN applications app ON app.id = ak.application_id "
            f"WHERE ak.owner_user = '{owner}' AND ak.tenant_id = '{TENANT}';"
        )
        db_count = int(db_rows[0][0]) if db_rows else -1
        _check(len(keys) == db_count,
               f"owner={owner} 条数与真库相等：接口 {len(keys)} vs 真库 {db_count}")
        _check(all(k.get("owner_user") == owner for k in keys),
               f"owner={owner} 返回行全部属于该 owner（无越权下发）")
        _check(all("status" in k and "created_at" in k for k in keys),
               f"owner={owner} 返回行含本轮新增字段 status / created_at")

    print("== B. 查看完整密钥（reveal；明文不回显） ==")
    status, body = _request(token, "GET", "/api/keys?owner=admin", port)
    admin_keys = json.loads(body) if status == 200 else []
    revealable = next((k for k in admin_keys if k.get("status") == "active"), None)
    if revealable is None:
        _check(False, "找不到可 reveal 的 active 密钥（跳过）")
    else:
        kid = revealable["id"]
        st, bd = _request(token, "GET", f"/api/keys/{kid}/reveal", port)
        if _check(st == 200, f"GET /api/keys/{kid}/reveal → 200 (实际 {st})"):
            obj = json.loads(bd)
            secret = obj.get("api_key") or ""
            digest = hashlib.sha256(secret.encode()).hexdigest()[:12] if secret else ""
            print(f"        key_id={kid} 明文长度={len(secret)} sha256前12={digest}")
            _check(bool(secret), "reveal 返回非空明文密钥")
            _check(obj.get("key_id") == kid, "reveal 返回的 key_id 与请求一致")

    print("== C. 对已吊销密钥点「启用」必须 409 ==")
    revoked = next((k for k in admin_keys if k.get("status") == "revoked"), None)
    if revoked is None:
        _check(False, "找不到 revoked 密钥（跳过）")
    else:
        rid = revoked["id"]
        before = _pg(f"SELECT COALESCE(status,'active'), enabled FROM api_keys WHERE id = {rid};")
        st, bd = _request(token, "POST", f"/api/keys/{rid}/enable", port, {})
        _check(st == 409, f"POST /api/keys/{rid}/enable（revoked）→ 409（实际 {st}）")
        print(f"        响应体: {bd.strip()[:200]}")
        after = _pg(f"SELECT COALESCE(status,'active'), enabled FROM api_keys WHERE id = {rid};")
        _check(before == after,
               f"409 分支未写库：status/enabled 前后一致 {before} → {after}")

    failed = [label for ok, label in _results if not ok]
    print("=" * 60)
    print(f"断言 {len(_results)} 条，通过 {len(_results) - len(failed)} 条，失败 {len(failed)} 条")
    for label in failed:
        print(f"  FAILED: {label}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
