#!/usr/bin/env python3
"""Validate the test environment before attributing failures to the gateway."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.request
from typing import Any

from result_contract import envelope, write_result


def check_http(url: str, headers: dict[str, str] | None = None) -> tuple[bool, str]:
    try:
        request = urllib.request.Request(url, headers=headers or {})
        with urllib.request.urlopen(request, timeout=3) as response:
            body = response.read(4096).decode("utf-8", errors="replace")
            if response.status < 200 or response.status >= 300:
                return False, f"HTTP {response.status}"
            return True, body[:200]
    except (OSError, urllib.error.URLError, urllib.error.HTTPError) as exc:
        return False, str(exc)


def check_db() -> tuple[bool, str, dict[str, Any]]:
    env = os.environ.copy()
    env.update(
        {
            "PGHOST": env.get("PGHOST", "localhost"),
            "PGPORT": env.get("PGPORT", "5432"),
            "PGUSER": env.get("PGUSER", "llm_gateway"),
            "PGDATABASE": env.get("PGDATABASE", env.get("PGDB", "llm_gateway")),
        }
    )
    sql = """
      SELECT json_build_object(
        'hot', to_regclass('public.request_logs_hot') IS NOT NULL,
        'bodies_hot', to_regclass('public.request_logs_bodies_hot') IS NOT NULL,
        'titles', to_regclass('public.session_titles') IS NOT NULL,
        'summaries', to_regclass('public.session_summaries') IS NOT NULL,
        'compression_strategy', EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='public' AND table_name='request_logs_hot'
            AND column_name='compression_strategy'
        ),
        'compression_meta', EXISTS (
          SELECT 1 FROM information_schema.columns
          WHERE table_schema='public' AND table_name='request_logs_hot'
            AND column_name='compression_meta'
        )
      )::text;
    """
    try:
        completed = subprocess.run(
            ["psql", "-X", "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql],
            env=env,
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        return False, str(exc), {}
    if completed.returncode != 0:
        return False, completed.stderr.strip() or "psql failed", {}
    raw = completed.stdout.strip().splitlines()
    if not raw:
        return False, "empty schema probe", {}
    try:
        details = json.loads(raw[-1])
    except json.JSONDecodeError as exc:
        return False, f"invalid schema probe: {exc}", {}
    missing = [key for key, value in details.items() if not value]
    if missing:
        return False, "missing: " + ", ".join(missing), details
    return True, "schema ok", details


def check_mocks(base_port: int, count: int) -> tuple[bool, str, dict[str, Any]]:
    healthy = 0
    failures: list[str] = []
    for port in range(base_port, base_port + count):
        ok, detail = check_http(f"http://127.0.0.1:{port}/healthz")
        if ok:
            healthy += 1
        else:
            failures.append(f"{port}: {detail}")
    if healthy == count:
        return (
            True,
            f"{healthy}/{count} mocks healthy",
            {"healthy": healthy, "total": count},
        )
    return (
        False,
        f"{healthy}/{count} mocks healthy",
        {
            "healthy": healthy,
            "total": count,
            "failures": failures[:10],
        },
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    # 2026-08-13: 默认端口 8781 会撞到本机别的 llm-gateway-go 容器 (PID 39265 等).
    # 改用 8793 (与 02-测试环境部署.md §3.2 + _lib.sh 一致).
    # 历史: S22/S23 第一轮失败 0/30 因为 prefight 也用默认 8781, 看似 healthz=200 但跑的不是同一份 binary,
    # 导致整个测试栈错位. 现统一到 8793, 任何环境如有冲突须显式 --gateway= 覆盖.
    parser.add_argument(
        "--gateway", default=os.environ.get("GATEWAY", "http://127.0.0.1:8793")
    )
    parser.add_argument("--output")
    parser.add_argument("--mock-base-port", type=int, default=19080)
    parser.add_argument("--mock-count", type=int, default=60)
    parser.add_argument("--skip-mocks", action="store_true")
    parser.add_argument("--skip-db", action="store_true")
    parser.add_argument("--require-metrics", action="store_true")
    args = parser.parse_args()

    failures: list[str] = []
    evidence: dict[str, Any] = {}
    gateway_ok, gateway_detail = check_http(args.gateway.rstrip("/") + "/healthz")
    evidence["healthz"] = {"ok": gateway_ok, "detail": gateway_detail}
    if not gateway_ok:
        failures.append("gateway healthz: " + gateway_detail)

    # 2026-08-13: identity check — confirm /healthz 响应与本仓 gateway binary 一致
    # (避免 8781 上有别人跑的旧版 llm-gateway-go 时 healthz 也返 200 误判).
    # 用 /healthz/full 拿版本号, 已知的 "2.5.x" 前缀表示本次构建.
    # 历史: S22/S23 第一轮失败 30/30, prefight 默认走 8781, 旧 binary 也返 200 OK,
    # 但路由走的不是同一份代码 — 这个 check 把这种 silent wrong-gateway 拦截掉.
    _gw_url = args.gateway.rstrip("/")
    _id_ok = True
    _id_detail = ""
    _has_admin = False
    try:
        # /healthz/full 是 admin 端点, 带 LLM_GATEWAY_ADMIN_API_KEY 自动鉴权
        _admin_token = os.environ.get("LLM_GATEWAY_ADMIN_API_KEY", "")
        _has_admin = bool(_admin_token)
        _headers = {"Authorization": f"Bearer {_admin_token}"} if _admin_token else {}
        _req = urllib.request.Request(f"{_gw_url}/healthz/full", headers=_headers)
        with urllib.request.urlopen(_req, timeout=3) as _resp:
            _body = _resp.read(2048).decode("utf-8", errors="replace")
        # 仅信任当前主分支的 2.5.x 版本标识, 防止撞到 7月31日二进制等.
        if not re.search(r'"version"\s*:\s*"2\.5\.', _body):
            _id_ok = False
            _id_detail = f"unexpected version in /healthz/full: {_body[:200]}"
    except urllib.error.HTTPError as _exc:
        # 401/403 = gateway 在, 但 admin token 不匹配 → 撞到了别人的 gateway (e.g. 7月31日二进制).
        # 这是 silent wrong-gateway 的关键信号, 必须 hard fail.
        if _exc.code in (401, 403) and _has_admin:
            _id_ok = False
            _id_detail = (
                f"HTTP {_exc.code} from {_gw_url}/healthz/full — gateway on this port "
                f"is rejecting LLM_GATEWAY_ADMIN_API_KEY. Likely hitting a stale binary."
            )
        else:
            _id_detail = f"identity probe skipped: HTTP {_exc.code} {_exc.reason}"
    except (OSError, urllib.error.URLError) as _exc:
        # /healthz/full 完全不可达 (端口空) — 留作 WARN, 因为 admin token 可能未配置.
        _id_detail = f"identity probe skipped: {_exc}"
    evidence["gateway_identity"] = {"ok": _id_ok, "detail": _id_detail}
    if not _id_ok:
        failures.append(f"gateway identity check failed: {_id_detail}")

    database_ok = True
    if not args.skip_db:
        database_ok, db_detail, db_evidence = check_db()
        evidence["database"] = db_evidence | {"detail": db_detail}
        if not database_ok:
            failures.append("database: " + db_detail)

    mocks_ok = None
    if not args.skip_mocks:
        mocks_ok, mock_detail, mock_evidence = check_mocks(
            args.mock_base_port, args.mock_count
        )
        evidence["mocks"] = mock_evidence | {"detail": mock_detail, "checked": True}
        if not mocks_ok:
            failures.append("mocks: " + mock_detail)
    else:
        evidence["mocks"] = {"checked": False, "detail": "skipped by request"}

    metrics_url = args.gateway.rstrip("/") + "/metrics"
    admin_token = os.environ.get("LLM_GATEWAY_ADMIN_API_KEY", "")
    headers = {"Authorization": f"Bearer {admin_token}"} if admin_token else {}
    metrics_ok, metrics_detail = check_http(metrics_url, headers)
    evidence["metrics"] = {
        "ok": metrics_ok,
        "detail": metrics_detail,
        "authenticated": bool(admin_token),
    }
    if args.require_metrics and not metrics_ok:
        failures.append("metrics: " + metrics_detail)

    status = "PASS" if not failures else "BLOCKED_ENVIRONMENT"
    result = envelope(
        "TEST_PREFLIGHT",
        "environment",
        status,
        checks={
            "gateway": gateway_ok,
            "gateway_identity": _id_ok,
            "database": database_ok,
            "mocks": mocks_ok,
            "metrics": metrics_ok if args.require_metrics else True,
        },
        evidence=evidence,
        failures=failures,
        parameters=vars(args),
        reason="; ".join(failures)
        if failures
        else "all required dependencies available",
    )
    print(json.dumps(result, ensure_ascii=False, indent=2))
    if args.output:
        write_result(args.output, result)
    return 0 if status == "PASS" else 2


if __name__ == "__main__":
    sys.exit(main())
