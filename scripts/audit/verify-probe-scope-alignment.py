#!/usr/bin/env python3
"""verify-probe-scope-alignment.py —— 2026-11-21 统计口径对齐轮的线上四面对拍。

在 211 本机执行（密钥/JWT 不出机器）：
  python3 verify-probe-scope-alignment.py <YYYY-MM-DD> [YYYY-MM-DD ...]

在 2026-11-20 verify-board-recon-faces.py（A/B/C 三面）基础上新增：
  D. request-logs「包含探测」对齐（本轮探测臂）：
     D1 /api/logs?include_probe=1 total_requests == 供应商对账 request_count
     D2 同窗 tokens(pt+ct) 相等
     D3 同窗 cost 分位级相等（探测臂费用 = usage_facts 同源）
     D4 响应 traffic_lane 披露正确（all / business）
  E. 会话统计对齐（dashboard ↔ session-analytics，单一真源构造相等）：
     E1 session-overview(aligned).total_sessions == session-analytics total_sessions
     E2 aligned.active_users == session-analytics active_users
     E3 aligned.internal_cost_usd == session-analytics internal_cost
     E4 aligned.external_credits == session-analytics external_cost
     E5 session-overview 缺省 probe_scope=excluded；include_probes=1 总数 ≥ excluded
注意：A/B/C/D 的对拍日必须是**已完结的 UTC 日**（对账快照 T+1 生成）；
E 的窗口用同一对 start_date/end_date（两侧同为上海日历日桶）。
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
    with urllib.request.urlopen(req, timeout=120) as resp:
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
        raise SystemExit("usage: verify-probe-scope-alignment.py <YYYY-MM-DD> [...]")
    token = mint_token()
    port = active_port(token)
    print(f"active port: {port}")
    failures = []

    def check(day: str, label: str, got, want) -> None:
        ok = got == want
        print(f"  [{'PASS' if ok else 'FAIL'}] {label}: got={got} want={want}")
        if not ok:
            failures.append(f"{day} {label}: got={got} want={want}")

    # E 窗口：与 A/B/C 同一批日期（session 两面都是上海日历日桶，逐日对拍）。
    for day in days:
        print(f"\n=== {day} (UTC 全天 / session 上海日历日) ===")
        board_ex = get(port, f"/api/admin/dashboard/board?start={day}&end={day}", token)
        board_in = get(port, f"/api/admin/dashboard/board?start={day}&end={day}&include_probes=1", token)
        recon_p = get(port, f"/api/admin/report-rollup/summary?start={day}&end={day}&view=provider", token)
        recon_i = get(port, f"/api/admin/report-rollup/summary?start={day}&end={day}&view=internal", token)
        logs_biz = get(port, "/api/logs?" + urllib.parse.urlencode({
            "from": f"{day}T00:00:00Z", "to": f"{day}T23:59:59.999Z", "page": 1, "page_size": 1,
        }), token)
        logs_all = get(port, "/api/logs?" + urllib.parse.urlencode({
            "from": f"{day}T00:00:00Z", "to": f"{day}T23:59:59.999Z", "page": 1, "page_size": 1,
            "include_probe": 1,
        }), token)

        s_ex = board_ex.get("summary") or {}
        s_in = board_in.get("summary") or {}
        tp = (recon_p.get("report") or {}).get("totals") or {}
        ti = (recon_i.get("report") or {}).get("totals") or {}
        agg_biz = logs_biz.get("aggregate") or {}
        agg_all = logs_all.get("aggregate") or {}

        print(f"  traffic_lane: biz={logs_biz.get('traffic_lane')} all={logs_all.get('traffic_lane')}")

        # A: 不含探测（既有面，回归确认）
        check(day, "A1 board(默认).total_requests == 内部对账.request_count",
              s_ex.get("total_requests"), ti.get("request_count"))
        check(day, "A3 request-logs(业务).total_requests == 内部对账.request_count",
              agg_biz.get("total_requests"), ti.get("request_count"))
        # D: 包含探测（本轮新面）
        check(day, "D1 request-logs(含探测).total_requests == 供应商对账.request_count",
              agg_all.get("total_requests"), tp.get("request_count"))
        check(day, "D2 request-logs(含探测).tokens(pt+ct) == 供应商对账 tokens",
              (agg_all.get("prompt_tokens") or 0) + (agg_all.get("completion_tokens") or 0),
              (tp.get("input_tokens") or 0) + (tp.get("output_tokens") or 0))
        cost_all = round(agg_all.get("cost_usd") or 0, 2)
        cost_p = round((tp.get("estimated_cost_cents") or 0) / 100.0, 2)
        check(day, "D3 request-logs(含探测).cost ≈ 供应商对账 cost_cents/100", cost_all, cost_p)
        check(day, "D4a request-logs(业务).traffic_lane == business",
              logs_biz.get("traffic_lane"), "business")
        check(day, "D4b request-logs(含探测).traffic_lane == all",
              logs_all.get("traffic_lane"), "all")
        # 差值方向
        probe_reqs = (agg_all.get("total_requests") or 0) - (agg_biz.get("total_requests") or 0)
        print(f"  [INFO] request-logs 探测流量(请求) = {probe_reqs}")

        # E: 会话统计对齐（同 start_date/end_date，上海日历日桶两侧一致）
        so = get(port, "/api/admin/dashboard/session-overview?" + urllib.parse.urlencode({
            "start_date": day, "end_date": day,
        }), token)
        sa = get(port, "/api/admin/session-analytics/overview?" + urllib.parse.urlencode({
            "start_date": day, "end_date": day,
        }), token)
        al = so.get("aligned") or {}
        sa_sum = sa.get("summary") or {}
        print(f"  probe_scope={so.get('probe_scope')} window_mode={so.get('window_mode')} "
              f"aligned_source={so.get('aligned_source')}")
        if not al:
            failures.append(f"{day} E0 aligned 块缺席（aligned_source={so.get('aligned_source')}）")
            continue
        check(day, "E1 aligned.total_sessions == session-analytics.total_sessions",
              al.get("total_sessions"), sa_sum.get("total_sessions"))
        check(day, "E2 aligned.active_users == session-analytics.active_users",
              al.get("active_users"), sa_sum.get("active_users"))
        check(day, "E3 aligned.internal_cost_usd == session-analytics.internal_cost",
              round(al.get("internal_cost_usd") or 0, 2), round(sa_sum.get("internal_cost") or 0, 2))
        check(day, "E4 aligned.external_credits == session-analytics.external_cost",
              al.get("external_credits"), sa_sum.get("external_cost"))
        check(day, "E5a session-overview(缺省).probe_scope == excluded",
              so.get("probe_scope"), "excluded")
        so_in = get(port, "/api/admin/dashboard/session-overview?" + urllib.parse.urlencode({
            "start_date": day, "end_date": day, "include_probes": 1,
        }), token)
        print(f"  [INFO] 会话宇宙：excluded={so.get('total_sessions')} "
              f"included={so_in.get('total_sessions')} "
              f"probe_scope={so_in.get('probe_scope')}")
        if (so_in.get("total_sessions") or 0) < (so.get("total_sessions") or 0):
            failures.append(f"{day} E5b included 会话数 < excluded（宇宙口径反转）")

    print(f"\n==== {'ALL PASS' if not failures else f'{len(failures)} FAILURES'} ====")
    for f in failures:
        print(" -", f)
    sys.exit(1 if failures else 0)


if __name__ == "__main__":
    main()
