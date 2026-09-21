#!/usr/bin/env python3
"""
LLM Gateway Mock Provider 综合系统测试 (7 场景)
================================================

场景列表:
  S1 正常操作     - healthy 模式基准流量, 100% 成功率
  S2 高延迟       - slow 模式延迟行为与路由规避
  S3 提供商故障   - server_error 模式故障转移 / 全故障错误传播
  S4 速率限制     - rate_limited 模式故障转移 / 429 传播
  S5 混合故障     - 多 provider 同时处于不同故障模式下的优雅降级
  S6 零停机部署   - 持续业务流量下执行 scripts/deploy-local.sh 真实红绿切换部署
  S7 持续高负载   - 20 并发持续 90 秒高负载稳定性

用法:
  python3 tests/mock-system-test/comprehensive_system_test.py [--scenarios 1,2,3,4,5,6,7] [--skip-deploy]

输出:
  tests/mock-system-test/reports/report-<ts>.md   (Markdown 报告)
  tests/mock-system-test/reports/results-<ts>.json (原始结果)
"""

from __future__ import annotations

import argparse
import asyncio
import json
import os
import platform
import statistics
import subprocess
import sys
import time
from collections import Counter
from datetime import datetime
from pathlib import Path

import aiohttp

PROJECT_ROOT = Path(__file__).resolve().parents[2]
REPORT_DIR = Path(__file__).resolve().parent / "reports"
DEPLOY_LOG = REPORT_DIR / "deploy-scenario6.log"

GATEWAY = "http://127.0.0.1:8782"
MOCK_PORTS = {1: 19080, 2: 19081, 3: 19082}
MOCK_BASE = {k: f"http://127.0.0.1:{v}" for k, v in MOCK_PORTS.items()}
API_KEY = "sk-e2e-test-1781898294"
CHAT_URL = f"{GATEWAY}/v1/chat/completions"

PG_CONTAINER = "llm-gateway-pg"
MOCK_CREDENTIAL_IDS = (70, 71, 72)


# ============================================================================
# 基础工具
# ============================================================================

def log(msg: str) -> None:
    print(f"[{datetime.now().strftime('%H:%M:%S')}] {msg}", flush=True)


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    values = sorted(values)
    idx = max(0, min(len(values) - 1, int(round((p / 100.0) * (len(values) - 1)))))
    return values[idx]


class LoadStats:
    """聚合一次压测的请求记录。"""

    def __init__(self) -> None:
        self.records: list[dict] = []

    def add(self, rec: dict) -> None:
        self.records.append(rec)

    @property
    def total(self) -> int:
        return len(self.records)

    @property
    def success(self) -> int:
        return sum(1 for r in self.records if r["ok"])

    @property
    def failures(self) -> int:
        return self.total - self.success

    def success_rate(self) -> float:
        return (self.success * 100.0 / self.total) if self.total else 0.0

    def latencies(self, ok_only: bool = True) -> list[float]:
        return [r["latency_ms"] for r in self.records if (r["ok"] or not ok_only)]

    def status_counter(self) -> Counter:
        return Counter(r["status"] for r in self.records)

    def identity_counter(self) -> Counter:
        return Counter(r["identity"] for r in self.records if r["ok"])

    def error_counter(self) -> Counter:
        return Counter(r["error_class"] for r in self.records if not r["ok"])

    def summary(self) -> dict:
        lat = self.latencies(ok_only=False)
        return {
            "total": self.total,
            "success": self.success,
            "failed": self.failures,
            "success_rate": round(self.success_rate(), 2),
            "latency_ms": {
                "min": round(min(lat), 1) if lat else 0,
                "p50": round(percentile(lat, 50), 1) if lat else 0,
                "p95": round(percentile(lat, 95), 1) if lat else 0,
                "p99": round(percentile(lat, 99), 1) if lat else 0,
                "max": round(max(lat), 1) if lat else 0,
                "avg": round(statistics.mean(lat), 1) if lat else 0,
            },
            "status_codes": dict(self.status_counter()),
            "served_by": dict(self.identity_counter()),
            "error_classes": dict(self.error_counter()),
        }


# ============================================================================
# Mock Provider 控制
# ============================================================================

async def mock_set_mode(session: aiohttp.ClientSession, mock_id: int, mode: str,
                        latency_min_ms: int | None = None,
                        latency_max_ms: int | None = None,
                        ttl_seconds: int = 1800) -> bool:
    body: dict = {"mode": mode, "ttl_seconds": ttl_seconds}
    if latency_min_ms is not None:
        body["latency_min_ms"] = latency_min_ms
    if latency_max_ms is not None:
        body["latency_max_ms"] = latency_max_ms
    try:
        async with session.post(f"{MOCK_BASE[mock_id]}/admin/state", json=body,
                                timeout=aiohttp.ClientTimeout(total=5)) as resp:
            return resp.status == 200
    except Exception as exc:
        log(f"  mock-{mock_id:02d} 状态设置失败: {exc}")
        return False


async def mocks_set_all(session: aiohttp.ClientSession, mode: str,
                        latency_min_ms: int | None = None,
                        latency_max_ms: int | None = None) -> bool:
    ok = True
    for mid in MOCK_PORTS:
        ok = await mock_set_mode(session, mid, mode, latency_min_ms, latency_max_ms) and ok
    return ok


async def mock_get_metrics(session: aiohttp.ClientSession, mock_id: int) -> dict | None:
    try:
        async with session.get(f"{MOCK_BASE[mock_id]}/admin/metrics",
                               timeout=aiohttp.ClientTimeout(total=5)) as resp:
            if resp.status == 200:
                return await resp.json()
    except Exception:
        pass
    return None


def db_recover_mock_bindings() -> None:
    """故障场景后恢复网关侧状态(清除冷却/熔断/节点探针排除标记)。

    node_probe_state: 上游失败会触发节点探针, 失败后 last_direct_ok=FALSE +
    指数退避的 next_retry_at, v_routable_credential_models 视图据此排除绑定;
    不清理该表, mock 恢复 healthy 后仍会被路由排除数分钟。
    """
    ids = ",".join(map(str, MOCK_CREDENTIAL_IDS))
    sql = f"""
UPDATE credential_model_bindings
SET available = TRUE, unavailable_reason = NULL, unavailable_at = NULL,
    consecutive_failures = 0, transient_failure_count = 0, updated_at = now()
WHERE credential_id IN ({ids});
UPDATE credentials
SET availability_state = 'ready', availability_recover_at = NULL,
    circuit_state = 'closed', health_status = 'healthy', updated_at = now()
WHERE id IN ({ids});
DELETE FROM node_probe_state WHERE credential_id IN ({ids});
"""
    try:
        subprocess.run(
            ["docker", "exec", "-i", PG_CONTAINER, "psql", "-U", "llm_gateway",
             "-d", "llm_gateway", "-q"],
            input=sql, text=True, capture_output=True, timeout=30, check=True,
        )
        log("  数据库侧 binding/credential/node_probe 状态已恢复")
    except Exception as exc:
        log(f"  数据库恢复失败: {exc}")


async def wait_e2e_recovery(session: aiohttp.ClientSession, max_wait: float = 45.0) -> bool:
    """恢复 healthy 后等待网关 E2E 路由恢复(冷却窗口/缓存过期/探针退避)。"""
    deadline = time.monotonic() + max_wait
    while time.monotonic() < deadline:
        # 连续 15 个请求需全部成功且覆盖至少 2 个 provider:
        # 单个成功可能只来自尚未被排除的残余节点, 不能证明全部恢复。
        identities: set[str] = set()
        all_ok = True
        for i in range(15):
            rec = await request_one(session, f"recovery-{i}")
            if not rec["ok"]:
                all_ok = False
                break
            if rec.get("identity"):
                identities.add(rec["identity"])
        if all_ok and len(identities) >= 2:
            return True
        await asyncio.sleep(3.0)
    return False


async def restore_all_healthy(session: aiohttp.ClientSession) -> bool:
    await mocks_set_all(session, "healthy", 150, 400)
    recovered = await wait_e2e_recovery(session)
    if not recovered:
        log("  E2E 未自愈, 执行数据库恢复...")
        db_recover_mock_bindings()
        await asyncio.sleep(6)
        recovered = await wait_e2e_recovery(session, max_wait=30)
    return recovered


# ============================================================================
# 请求与压测
# ============================================================================

def classify(rec_status: str, exc: str | None) -> str:
    if exc:
        if "timeout" in exc.lower():
            return "timeout"
        if "connect" in exc.lower() or "refused" in exc.lower() or "reset" in exc.lower():
            return "connection_error"
        return "client_exception"
    if rec_status.startswith("2"):
        return "-"
    if rec_status == "429":
        return "rate_limited_429"
    if rec_status.startswith("5"):
        return "server_error_5xx"
    return f"http_{rec_status}"


async def request_one(session: aiohttp.ClientSession, tag: str,
                      timeout_s: float = 30.0) -> dict:
    payload = {
        "model": "gpt-4",
        "messages": [{"role": "user", "content": f"mst-{tag}-{time.time_ns()}"}],
        "max_tokens": 20,
    }
    headers = {
        "Authorization": f"Bearer {API_KEY}",
        "Content-Type": "application/json",
    }
    start = time.monotonic()
    status, identity, err = "000", None, None
    try:
        async with session.post(CHAT_URL, json=payload, headers=headers,
                                timeout=aiohttp.ClientTimeout(total=timeout_s)) as resp:
            status = str(resp.status)
            try:
                body = await resp.json()
                identity = body.get("_mock_identity")
                if resp.status >= 400:
                    err = (body.get("error") or {}).get("message", "http-error")[:120]
            except Exception:
                err = "non-json-body"
    except Exception as exc:
        err = f"{type(exc).__name__}: {exc}"[:160]
    latency_ms = (time.monotonic() - start) * 1000.0
    ok = status.startswith("2")
    return {
        "tag": tag,
        "ok": ok,
        "status": status,
        "identity": identity,
        "latency_ms": latency_ms,
        "error": err,
        "error_class": classify(status, err if status == "000" else None),
        "ts": time.time(),
    }


async def run_load(session: aiohttp.ClientSession, n_requests: int, concurrency: int,
                   timeout_s: float = 30.0,
                   duration_s: float | None = None,
                   retry_on: tuple[str, ...] = ("connection_error", "server_error_5xx"),
                   label: str = "",
                   stop_event: asyncio.Event | None = None,
                   max_retries: int = 2,
                   backoff_base: float = 0.2) -> tuple[LoadStats, LoadStats]:
    """执行压测。

    duration_s: 若给定, 则按时间驱动(持续流量), 否则按请求数驱动。
    stop_event: 外部协作式停止信号(优先于 duration_s)。
    retry_on:   对这些 error_class 执行 SDK 式重试(默认 2 次, 200ms 退避);
                返回 (raw_stats, user_stats): raw 为首次尝试结果,
                user 为重试后的最终用户可见结果。
                默认与主流 OpenAI SDK 重试语义一致(连接错误/5xx)。
    """
    raw, user = LoadStats(), LoadStats()
    counter = {"n": 0}
    lock = asyncio.Lock()

    async def next_id() -> int:
        async with lock:
            counter["n"] += 1
            return counter["n"]

    stop_at = (time.monotonic() + duration_s) if duration_s else None

    def should_stop() -> bool:
        if stop_event is not None and stop_event.is_set():
            return True
        if stop_at is not None and time.monotonic() >= stop_at:
            return True
        return False

    async def worker(wid: int) -> None:
        while True:
            rid = await next_id()
            if stop_at is None and stop_event is None and rid > n_requests:
                return
            if should_stop():
                return
            rec = await request_one(session, f"{label}-w{wid}-{rid}", timeout_s)
            raw.add(rec)
            final = rec
            retries = 0
            while (not final["ok"] and retry_on
                   and final["error_class"] in retry_on and retries < max_retries):
                retries += 1
                await asyncio.sleep(backoff_base * (2 ** (retries - 1)))
                final = await request_one(session, f"{label}-w{wid}-{rid}-r{retries}", timeout_s)
                final["retries"] = retries
            user.add(final)

    sem = asyncio.Semaphore(concurrency)

    async def guarded(wid: int) -> None:
        async with sem:
            await worker(wid)

    await asyncio.gather(*(guarded(w) for w in range(concurrency)))
    return raw, user


async def gateway_health(session: aiohttp.ClientSession) -> tuple[bool, str | None]:
    try:
        async with session.get(f"{GATEWAY}/healthz",
                               timeout=aiohttp.ClientTimeout(total=2)) as resp:
            if resp.status == 200:
                return True, None
            return False, f"http-{resp.status}"
    except Exception as exc:
        return False, f"{type(exc).__name__}"


async def gateway_version(session: aiohttp.ClientSession) -> dict:
    try:
        async with session.get(f"{GATEWAY}/version",
                               timeout=aiohttp.ClientTimeout(total=3)) as resp:
            return await resp.json()
    except Exception:
        return {}


# ============================================================================
# 场景实现
# ============================================================================

SCENARIO_RESULTS: list[dict] = []


def record_scenario(name: str, passed: bool, details: dict, notes: str = "") -> None:
    SCENARIO_RESULTS.append({
        "scenario": name, "passed": passed, "details": details, "notes": notes,
    })
    mark = "✅ PASS" if passed else "❌ FAIL"
    log(f"  ⇒ {name}: {mark}")


async def scenario_1_normal(session: aiohttp.ClientSession) -> None:
    log("── S1 正常操作 ──────────────────────────────")
    await mocks_set_all(session, "healthy", 150, 400)
    await asyncio.sleep(1)
    stats_raw, stats = await run_load(session, 200, concurrency=10, label="s1")
    s, s_raw = stats.summary(), stats_raw.summary()
    identities = stats.identity_counter()
    passed = (stats.success_rate() >= 99.0
              and s["latency_ms"]["p95"] < 5000
              and len(identities) >= 1)
    record_scenario("S1 正常操作", passed,
                    {"user_visible": s, "raw_first_attempt": s_raw},
                    notes=f"用户可见成功率≥99% 且 p95<5s (SDK 重试语义); mock 分布: {dict(identities)}")


async def scenario_2_high_latency(session: aiohttp.ClientSession) -> None:
    log("── S2 高延迟 ────────────────────────────────")
    # Phase A: 仅 mock-01 高延迟, 其余健康 → 网关应尽量路由到健康节点
    await mock_set_mode(session, 1, "slow", 2000, 3000)
    await asyncio.sleep(1)
    _, stats_a = await run_load(session, 60, concurrency=5, label="s2a")
    sa = stats_a.summary()
    # Phase B: 全部高延迟 → 请求仍然成功, 延迟显著上升
    await mocks_set_all(session, "slow", 2000, 3500)
    await asyncio.sleep(1)
    _, stats_b = await run_load(session, 30, concurrency=5, timeout_s=45, label="s2b")
    sb = stats_b.summary()
    lat_b = stats_b.latencies()
    passed = (stats_a.success_rate() == 100.0
              and stats_b.success_rate() == 100.0
              and len(lat_b) > 0 and min(lat_b) >= 1900)
    record_scenario("S2 高延迟", passed,
                    {"phase_a_partial_slow": sa, "phase_b_all_slow": sb},
                    notes="A: 100% 成功(慢节点被规避或可容忍); B: 100% 成功且最小延迟≥1.9s (用户可见)")
    await restore_all_healthy(session)


async def scenario_3_provider_failure(session: aiohttp.ClientSession) -> None:
    log("── S3 提供商故障 ────────────────────────────")
    # Phase A: 单 provider 500 错误 → 期望故障转移到健康节点
    await mock_set_mode(session, 1, "server_error")
    await asyncio.sleep(1)
    _, stats_a = await run_load(session, 60, concurrency=6, label="s3a")
    sa = stats_a.summary()
    # Phase B: 两个 provider 故障 → 全部流量走 mock-03
    await mock_set_mode(session, 2, "server_error")
    await asyncio.sleep(1)
    _, stats_b = await run_load(session, 40, concurrency=6, label="s3b")
    sb = stats_b.summary()
    # Phase C: 全部确定性故障(connection_refused → 立即 503) →
    # 期望 5xx 错误传播到客户端(不允许错误地成功); 关闭重试以观察传播。
    # 客户端超时放宽到 35s: 网关对 503 有重试/退避链, 观测其上界。
    await mocks_set_all(session, "connection_refused")
    await asyncio.sleep(1)
    _, stats_c = await run_load(session, 15, concurrency=5, retry_on=(),
                                timeout_s=35, label="s3c")
    sc = stats_c.summary()
    c_all_failed_fast = (stats_c.success_rate() == 0.0
                         and max(stats_c.latencies(ok_only=False), default=0) < 35000)
    passed = (stats_a.success_rate() >= 95.0
              and stats_b.success_rate() >= 95.0
              and c_all_failed_fast)
    record_scenario("S3 提供商故障", passed,
                    {"phase_a_one_degraded": sa, "phase_b_two_degraded": sb, "phase_c_all_down": sc},
                    notes="A/B: 1-2 个 provider 50% 错误率下用户可见成功率≥95% (故障转移+重试); C: 全 provider 确定性宕机时 5xx 快速传播(无挂起)")
    await restore_all_healthy(session)


async def scenario_4_rate_limit(session: aiohttp.ClientSession) -> None:
    log("── S4 速率限制 ──────────────────────────────")
    await mock_set_mode(session, 1, "rate_limited")
    await asyncio.sleep(1)
    _, stats_a = await run_load(session, 60, concurrency=6, label="s4a")
    sa = stats_a.summary()
    await mocks_set_all(session, "rate_limited")
    await asyncio.sleep(1)
    # 关闭重试: 全局限流时观察错误如何传播(网关可能将上游 429 转换为 503)
    _, stats_b = await run_load(session, 15, concurrency=5, retry_on=(),
                                timeout_s=35, label="s4b")
    sb = stats_b.summary()
    b_propagates = (stats_b.success_rate() == 0.0
                    and max(stats_b.latencies(ok_only=False), default=0) < 35000)
    passed = (stats_a.success_rate() >= 95.0 and b_propagates)
    record_scenario("S4 速率限制", passed,
                    {"phase_a_one_limited": sa, "phase_b_all_limited": sb},
                    notes="A: 限流节点被规避/重试, 用户可见成功率≥95%; B: 全局限流时错误传播(0% 成功、无挂起; 观测上游 429 是否被转换为 503)")
    await restore_all_healthy(session)


async def scenario_5_mixed_failures(session: aiohttp.ClientSession) -> None:
    log("── S5 混合故障 ──────────────────────────────")
    # Phase A: error + slow + healthy 并存
    await mock_set_mode(session, 1, "server_error")
    await mock_set_mode(session, 2, "slow", 1000, 2000)
    await mock_set_mode(session, 3, "healthy", 150, 400)
    await asyncio.sleep(1)
    _, stats_a = await run_load(session, 100, concurrency=8, timeout_s=45, label="s5a")
    sa = stats_a.summary()
    # Phase B: 雪上加霜 - 健康节点变 flaky(50% 失败)
    await mock_set_mode(session, 3, "flaky")
    await asyncio.sleep(1)
    _, stats_b = await run_load(session, 60, concurrency=8, timeout_s=45, label="s5b")
    sb = stats_b.summary()
    b_degraded_gracefully = (
        stats_b.success_rate() > 0.0
        and max(stats_b.latencies(ok_only=False), default=0) < 45000
    )
    passed = (stats_a.success_rate() >= 90.0 and b_degraded_gracefully)
    record_scenario("S5 混合故障", passed,
                    {"phase_a_error_slow_healthy": sa, "phase_b_all_degraded": sb},
                    notes="A: 混合故障下成功率≥90%; B: 全链路劣化时优雅降级(仍有成功且无挂起)")
    await restore_all_healthy(session)


async def scenario_6_zero_downtime_deploy(session: aiohttp.ClientSession) -> None:
    log("── S6 零停机部署 (deploy-local.sh 红绿切换) ──")
    version_before = await gateway_version(session)
    log(f"  部署前版本: {version_before.get('version')}")

    health_events: list[dict] = []
    health_stop = asyncio.Event()

    async def health_monitor() -> None:
        while not health_stop.is_set():
            ok, reason = await gateway_health(session)
            health_events.append({"ts": time.time(), "ok": ok, "reason": reason})
            await asyncio.sleep(0.5)

    retry_policy = ("connection_error", "server_error_5xx")
    load_stop = asyncio.Event()
    load_task = asyncio.create_task(
        run_load(session, 0, concurrency=3, retry_on=retry_policy, label="s6",
                 stop_event=load_stop, max_retries=4, backoff_base=0.4))
    monitor_task = asyncio.create_task(health_monitor())
    await asyncio.sleep(3)  # 让流量先稳定

    deploy_started = time.time()
    log("  启动 deploy-local.sh deploy --no-frontend ...")
    DEPLOY_LOG.parent.mkdir(parents=True, exist_ok=True)
    # 注入当前运行实例的部署环境(LLM_GATEWAY_SECRET_KEY 等), 保证密钥连续性;
    # 与 2026-09-05 事故一致: 缺少 SECRET_KEY 的部署会被守卫拒绝(空 key 无法签发 admin 会话)。
    # 在 Python 侧解析 env 文件(而非 shell source): 运行环境文件含未加引号的
    # 特殊字符(如密码中的 &), shell source 会把赋值截断成命令。
    run_env_file = (Path.home() / "kaixuan/llm-gateway-go/run/active-port")
    port = run_env_file.read_text().strip() if run_env_file.exists() else "8782"
    env_file = Path.home() / f"kaixuan/llm-gateway-go/run/llm-gateway-local-{port}.env"
    deploy_env = dict(os.environ)
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if not line or line.startswith("#") or "=" not in line:
                continue
            key, value = line.split("=", 1)
            if key in ("LLM_GATEWAY_SECRET_KEY",
                       "LLM_GATEWAY_CREDENTIAL_ENCRYPTION_KEY",
                       "LLM_GATEWAY_ADMIN_API_KEY",
                       "LLM_GATEWAY_ADMIN_USER",
                       "LLM_GATEWAY_ADMIN_PASSWORD"):
                deploy_env[key] = value
        log(f"  已注入部署环境({env_file.name}: 密钥/admin 凭证)")
    else:
        log(f"  ⚠ 未找到运行环境文件 {env_file}, 使用当前环境部署")
    with open(DEPLOY_LOG, "w") as logf:
        proc = await asyncio.create_subprocess_exec(
            "bash", "scripts/deploy-local.sh", "deploy", "--no-frontend",
            cwd=str(PROJECT_ROOT), env=deploy_env,
            stdout=logf, stderr=subprocess.STDOUT,
        )
        try:
            await asyncio.wait_for(proc.wait(), timeout=1200)
        except asyncio.TimeoutError:
            proc.kill()
            log("  ✗ 部署超时(1200s), 已终止")
    deploy_rc = proc.returncode
    deploy_duration = time.time() - deploy_started
    log(f"  部署进程退出 rc={deploy_rc}, 耗时 {deploy_duration:.0f}s")

    # 部署结束后继续压 10s, 确认流量恢复
    await asyncio.sleep(10)
    health_stop.set()
    await monitor_task
    load_stop.set()
    stats_raw, stats_user = await load_task
    version_after = await gateway_version(session)
    log(f"  部署后版本: {version_after.get('version')}")

    # 健康缺口统计
    unhealthy = [e for e in health_events if not e["ok"]]
    health_gap_s = 0.0
    gap_start = gap_end = None
    if unhealthy:
        gap_start = unhealthy[0]["ts"] - 2.0
        gap_end = unhealthy[-1]["ts"] + 5.0
        health_gap_s = unhealthy[-1]["ts"] - unhealthy[0]["ts"] + 0.5

    # 缺口窗口外的用户可见失败(部署机制自身导致的失败必须为零)
    if gap_start is not None:
        out_of_gap_failures = sum(
            1 for r in stats_user.records
            if not r["ok"] and not (gap_start <= r["ts"] <= gap_end))
    else:
        out_of_gap_failures = stats_user.failures

    raw_fail = stats_raw.failures
    user_fail = stats_user.failures
    total_user = stats_user.total
    throughput = total_user / (deploy_duration + 13)

    version_changed = (version_before.get("build_seq") != version_after.get("build_seq"))
    passed = (deploy_rc == 0
              and version_changed
              and health_gap_s <= 30.0
              and out_of_gap_failures == 0
              and stats_user.success_rate() >= 98.0)
    record_scenario("S6 零停机部署", passed, {
        "deploy_rc": deploy_rc,
        "deploy_duration_s": round(deploy_duration, 1),
        "version_before": version_before.get("version"),
        "version_after": version_after.get("version"),
        "build_seq_changed": version_changed,
        "traffic_total": total_user,
        "traffic_success_rate": round(stats_user.success_rate(), 2),
        "user_visible_failures": user_fail,
        "out_of_gap_failures": out_of_gap_failures,
        "raw_failures_before_retry": raw_fail,
        "health_probe_unhealthy_count": len(unhealthy),
        "health_gap_s": round(health_gap_s, 1),
        "throughput_rps": round(throughput, 2),
    }, notes="真实执行 scripts/deploy-local.sh deploy(候选 8781 预热+健康/就绪/版本/解密四重验证后切换 8782); "
             "判定: 部署成功+版本切换+健康缺口≤30s(受控重启窗口, 本地 Docker 模式无外部代理)+缺口外用户可见失败=0+总成功率≥98%")


async def scenario_7_sustained_load(session: aiohttp.ClientSession) -> None:
    log("── S7 持续高负载 ────────────────────────────")
    await mocks_set_all(session, "healthy", 150, 400)
    await asyncio.sleep(2)

    mid_cpu = subprocess.run(
        ["docker", "stats", "--no-stream", "--format",
         "{{.Name}} cpu={{.CPUPerc}} mem={{.MemUsage}}", "llm-gateway-local-8782"],
        capture_output=True, text=True, timeout=30,
    ).stdout.strip()

    duration = 90
    _, stats = await run_load(session, 0, concurrency=20, duration_s=duration, label="s7")
    s = stats.summary()
    throughput = stats.total / duration
    identities = stats.identity_counter()

    ok_after, _ = await gateway_health(session)
    passed = (stats.success_rate() >= 99.0
              and throughput >= 10.0
              and ok_after)
    record_scenario("S7 持续高负载", passed, {
        **s,
        "duration_s": duration,
        "concurrency": 20,
        "throughput_rps": round(throughput, 2),
        "gateway_container_stats": mid_cpu,
        "gateway_healthy_after": ok_after,
    }, notes="20 并发持续 90s; 成功率≥99%, 吞吐≥10 rps, 负载后网关健康")


# ============================================================================
# 预检 & 报告
# ============================================================================

async def preflight(session: aiohttp.ClientSession) -> bool:
    log("── 预检 ─────────────────────────────────────")
    ok, _ = await gateway_health(session)
    log(f"  网关健康: {ok}")
    mocks_ok = True
    for mid, base in MOCK_BASE.items():
        try:
            async with session.get(f"{base}/healthz",
                                   timeout=aiohttp.ClientTimeout(total=3)) as resp:
                body = await resp.json()
                log(f"  mock-{mid:02d} ({base}): {body.get('mode')}")
                mocks_ok = mocks_ok and resp.status == 200
        except Exception as exc:
            log(f"  mock-{mid:02d} 不可达: {exc}")
            mocks_ok = False
    rec = await request_one(session, "preflight")
    log(f"  E2E 路由冒烟: {'✅ ' + str(rec.get('identity')) if rec['ok'] else '❌ ' + str(rec.get('error'))}")
    return ok and mocks_ok and rec["ok"]


def write_report(results: list[dict], env_info: dict) -> Path:
    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now().strftime("%Y%m%d-%H%M%S")
    md_path = REPORT_DIR / f"report-{ts}.md"
    json_path = REPORT_DIR / f"results-{ts}.json"

    total_pass = sum(1 for r in results if r["passed"])

    lines: list[str] = []
    lines.append("# LLM Gateway Mock Provider 综合系统测试报告")
    lines.append("")
    lines.append(f"**测试时间**: {env_info['timestamp']}  ")
    lines.append(f"**网关**: {GATEWAY} (`{env_info['gateway_version']}`)  ")
    lines.append(f"**Mock Providers**: 3 实例 (19080/19081/19082, server-v2.py)  ")
    lines.append(f"**平台**: {env_info['platform']}  ")
    lines.append("")
    lines.append("## 总体结果")
    lines.append("")
    lines.append("| 场景 | 结果 |")
    lines.append("|------|------|")
    for r in results:
        mark = "✅ 通过" if r["passed"] else "❌ 失败"
        lines.append(f"| {r['scenario']} | {mark} |")
    lines.append(f"| **合计** | **{total_pass}/{len(results)} 通过** |")
    lines.append("")

    for r in results:
        lines.append(f"## {r['scenario']}")
        lines.append("")
        if r["notes"]:
            lines.append(f"**判定标准**: {r['notes']}")
            lines.append("")
        lines.append("```json")
        lines.append(json.dumps(r["details"], ensure_ascii=False, indent=2))
        lines.append("```")
        lines.append("")

    if any("S6" in r["scenario"] for r in results):
        lines.append("### S6 部署日志摘录")
        lines.append("")
        if DEPLOY_LOG.exists():
            deploy_text = DEPLOY_LOG.read_text(errors="replace")
            key_lines = [l for l in deploy_text.splitlines()
                         if any(k in l for k in ("candidate", "active", "健康", "readyz",
                                                 "healthz", "VERIFY", "cutover", "smoke",
                                                 "deploy-local]", "version", "切")) ]
            lines.append("```text")
            lines.extend(key_lines[-60:])
            lines.append("```")
        else:
            lines.append("(日志缺失)")
        lines.append("")

    lines.append("## 结论")
    lines.append("")
    if total_pass == len(results):
        lines.append("全部 7 个测试场景通过。网关在 Mock Provider 注入的各类故障模式下")
        lines.append("表现出预期的容错行为(故障转移、限流传播、优雅降级),")
        lines.append("并通过 deploy-local.sh 完成了红绿切换部署验证。")
    else:
        failed = [r["scenario"] for r in results if not r["passed"]]
        lines.append(f"以下场景未通过: {', '.join(failed)}。详见各场景数据。")
    lines.append("")

    md_path.write_text("\n".join(lines), encoding="utf-8")
    json_path.write_text(json.dumps({
        "env": env_info, "results": results,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    return md_path


async def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--scenarios", default="1,2,3,4,5,6,7")
    parser.add_argument("--skip-deploy", action="store_true",
                        help="跳过场景 6 的真实部署(仅调试用)")
    args = parser.parse_args()
    selected = {int(x) for x in args.scenarios.split(",") if x.strip()}
    if args.skip_deploy:
        selected.discard(6)

    connector = aiohttp.TCPConnector(limit=64)
    async with aiohttp.ClientSession(connector=connector) as session:
        if not await preflight(session):
            log("预检失败, 中止测试")
            return 2

        impls = {
            1: scenario_1_normal, 2: scenario_2_high_latency,
            3: scenario_3_provider_failure, 4: scenario_4_rate_limit,
            5: scenario_5_mixed_failures, 6: scenario_6_zero_downtime_deploy,
            7: scenario_7_sustained_load,
        }
        for sid in sorted(selected):
            await impls[sid](session)
            await asyncio.sleep(2)

    env_info = {
        "timestamp": datetime.now().strftime("%Y-%m-%d %H:%M:%S"),
        "gateway_version": json.dumps({}),
        "platform": f"{platform.system()} {platform.machine()} / py{platform.python_version()}",
    }
    async with aiohttp.ClientSession() as session:
        v = await gateway_version(session)
        env_info["gateway_version"] = v.get("version", "unknown")

    report = write_report(SCENARIO_RESULTS, env_info)
    log(f"报告: {report}")
    all_pass = all(r["passed"] for r in SCENARIO_RESULTS) and len(SCENARIO_RESULTS) == len(selected)
    log(f"测试完成: {sum(1 for r in SCENARIO_RESULTS if r['passed'])}/{len(SCENARIO_RESULTS)} 通过")
    return 0 if all_pass else 1


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
