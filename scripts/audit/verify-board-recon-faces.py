#!/usr/bin/env python3
"""verify-faces.py —— 2026-11-20 看板对账口径对齐轮的线上三面对拍。

在 211 本机执行（密钥/JWT 不出机器）：
  python3 verify-faces.py <YYYY-MM-DD> [YYYY-MM-DD ...]

对拍矩阵（窗口 = 每个 UTC 日全天）：
  A. board 默认（不含探测）   == 内部对账 totals         == request-logs 业务 lane 计数
  B. board include_probes=1   == 供应商对账 totals
  C. 总流量 tokens / 总费用 cost 同窗口一致（A/B 两口径都对）
判据：逐字段相等；request 数允许 =0 差（对拍的是已完结的过去日）。
"""
import base64
import hashlib
import hmac
import json
import subprocess
import sys
import time
import urllib.request
import urllib.parse

CONTAINER_CANDIDATES = ("llm-gateway-211-a", "llm-gateway-211-b")
USER_ID, USERNAME, TENANT, ROLE = 41, "admin", "default", "super_admin"


def _running_container() -> str:
    out = subprocess.run(["docker", "ps", "--format", "{{.Names}}"],
                         capture_output=True, text=True, check=False).stdout.split()
    for name in CONTAINER_CANDIDATES:
        if name in out:
            return name
    raise SystemExit("没有在跑的 llm-gateway-211-* 容器")


def _b64(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def mint_token(ttl: int = 3600) -> str:
    container = _running_container()
    secret = subprocess.check_output(
        ["docker", "exec", container, "printenv", "LLM_GATEWAY_SECRET_KEY"]).decode().strip()
    header = _b64(json.dumps({"alg": "HS256", "typ": "JWT"}, separators=(",", ":")).encode())
    now = int(time.time())
    payload = _b64(json.dumps({
        "user_id": USER_ID, "tenant_id": TENANT, "username": USERNAME, "role": ROLE,
        "exp": now + ttl, "iat": now, "iss": "llm-gateway", "aud": ["llm-gateway-api"],
    }, separators=(",", ":")).encode())
    sig = _b64(hmac.new(secret.encode(), f"{header}.{payload}".encode(), hashlib.sha256).digest())
    return f"{header}.{payload}.{sig}"


def get(port: int, path: str, token: str) -> dict:
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}",
                                 headers={"Authorization": f"Bearer {token}"})
    with urllib.request.urlopen(req, timeout=90) as resp:
        return json.loads(resp.read().decode("utf-8", "replace"))


def active_port(token: str) -> int:
    for port in (8783, 8781):
        try:
            get(port, "/healthz", token)
            return port
        except Exception:
            continue
    raise SystemExit("8781/8783 都不可达")


def main() -> None:
    days = sys.argv[1:]
    if not days:
        raise SystemExit("usage: verify-faces.py <YYYY-MM-DD> [...]")
    token = mint_token()
    port = active_port(token)
    print(f"active port: {port}")
    failures = []

    def check(day: str, label: str, got, want) -> None:
        ok = got == want
        print(f"  [{'PASS' if ok else 'FAIL'}] {label}: got={got} want={want}")
        if not ok:
            failures.append(f"{day} {label}: got={got} want={want}")

    for day in days:
        print(f"\n=== {day} (UTC 全天) ===")
        board_ex = get(port, f"/api/admin/dashboard/board?start={day}&end={day}", token)
        board_in = get(port, f"/api/admin/dashboard/board?start={day}&end={day}&include_probes=1", token)
        recon_p = get(port, f"/api/admin/report-rollup/summary?start={day}&end={day}&view=provider", token)
        recon_i = get(port, f"/api/admin/report-rollup/summary?start={day}&end={day}&view=internal", token)
        logs = get(port, "/api/logs?" + urllib.parse.urlencode({
            "from": f"{day}T00:00:00Z", "to": f"{day}T23:59:59.999Z", "page": 1, "page_size": 1,
        }), token)

        s_ex = board_ex.get("summary") or {}
        s_in = board_in.get("summary") or {}
        tp = (recon_p.get("report") or {}).get("totals") or {}
        ti = (recon_i.get("report") or {}).get("totals") or {}
        agg = logs.get("aggregate") or {}

        print(f"  probe_scope: exclude={board_ex.get('probe_scope')} include={board_in.get('probe_scope')} "
              f"summary_source={s_ex.get('summary_source')}")

        # A: 不含探测 == 内部对账 == request-logs
        check(day, "A1 board(默认).total_requests == 内部对账.request_count",
              s_ex.get("total_requests"), ti.get("request_count"))
        check(day, "A2 board(默认).total_tokens == 内部对账 tokens(pt+ct)",
              s_ex.get("total_tokens"), (ti.get("input_tokens") or 0) + (ti.get("output_tokens") or 0))
        check(day, "A3 request-logs.total_requests == 内部对账.request_count",
              agg.get("total_requests"), ti.get("request_count"))
        cost_ex = round(s_ex.get("total_cost_usd") or 0, 2)
        cost_i = round(((ti.get("estimated_cost_cents") or 0)) / 100.0, 2)
        check(day, "A4 board(默认).cost ≈ 内部对账 cost_cents/100",
              cost_ex, cost_i)

        # B: 含探测 == 供应商对账
        check(day, "B1 board(include).total_requests == 供应商对账.request_count",
              s_in.get("total_requests"), tp.get("request_count"))
        check(day, "B2 board(include).total_tokens == 供应商对账 tokens(pt+ct)",
              s_in.get("total_tokens"), (tp.get("input_tokens") or 0) + (tp.get("output_tokens") or 0))
        cost_in = round(s_in.get("total_cost_usd") or 0, 2)
        cost_p = round(((tp.get("estimated_cost_cents") or 0)) / 100.0, 2)
        check(day, "B3 board(include).cost ≈ 供应商对账 cost_cents/100",
              cost_in, cost_p)

        # C: 差值方向（include - exclude = 探测流量，应 ≥ 0）
        probe_reqs = (s_in.get("total_requests") or 0) - (s_ex.get("total_requests") or 0)
        print(f"  [INFO] 探测流量(请求) = {probe_reqs}")

    print(f"\n==== {'ALL PASS' if not failures else f'{len(failures)} FAILURES'} ====")
    for f in failures:
        print(" -", f)
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
