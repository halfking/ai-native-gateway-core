#!/usr/bin/env python3
"""mock-only gateway stress harness — production cmd/gateway + fake vendors.

Scope (deliberately honest):
  * The gateway under test IS the production cmd/gateway binary running in
    the lgw-mock-only compose stack. This is NOT the buffered test gateway
    from tests/stress/gateway — that one stays a unit-level harness.
  * Every upstream is a mock-vendor-* container. Real vendor names appear
    nowhere: models are mock-model-{fast,long,stream}, the data-plane key
    is the stack-local mock-only-gateway-key.
  * Fault injection goes through each mock's POST /admin/state; gateway-side
    cooldown/probe recovery goes through the gateway's own admin API
    (node-probe-state/reset + reset-availability + recover) — the same
    levers an operator has, nothing bypasses the product.

Usage (inside the `clients` container, or anywhere that can reach the net):
  python3 mock_only_loadtest.py                    # full suite
  python3 mock_only_loadtest.py --scenarios S01,S12,S13
  python3 mock_only_loadtest.py --quick            # reduced counts

Outputs: reports/mock-only-<ts>.json and .md; exit 0 only if all selected
scenarios pass.
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import re
import socket
import statistics
import sys
import threading
import time
import urllib.parse
import urllib.request
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

GATEWAY = os.environ.get("GATEWAY_URL", "http://gateway:8781")
API_KEY = os.environ.get("GATEWAY_API_KEY", "mock-only-gateway-key")
ADMIN_USER = os.environ.get("ADMIN_USER", "admin")
ADMIN_PASSWORD = os.environ.get("ADMIN_PASSWORD", "mock-only-admin-password")
MOCK_URLS = {
    "alpha": os.environ.get("MOCK_ALPHA_URL", "http://mock-alpha:18101"),
    "beta": os.environ.get("MOCK_BETA_URL", "http://mock-beta:18101"),
    "gamma": os.environ.get("MOCK_GAMMA_URL", "http://mock-gamma:18101"),
    "delta": os.environ.get("MOCK_DELTA_URL", "http://mock-delta:18101"),
}
VENDOR_IDS = {95001: "alpha", 95002: "beta", 95003: "gamma", 95004: "delta"}
MODELS = ["mock-model-fast", "mock-model-long", "mock-model-stream"]
REPORT_DIR = Path(os.environ.get("MOCK_REPORT_DIR", "reports"))

VENDOR_RE = re.compile(r"\[mock-vendor-(\w+) reply\]")
ID_RE = re.compile(r"chatcmpl-mock-vendor-(\w+)-")

SCENARIOS: list[dict] = []
_print_lock = threading.Lock()


def log(msg: str) -> None:
    with _print_lock:
        print(f"[{datetime.now().strftime('%H:%M:%S')}] {msg}", flush=True)


# ══════════════════════════════════════════════════════════════════════
# HTTP plumbing (stdlib; one keep-alive connection per worker thread)
# ══════════════════════════════════════════════════════════════════════

_thread_local = threading.local()


def _conn() -> http.client.HTTPConnection:
    conn = getattr(_thread_local, "conn", None)
    if conn is None:
        parsed = urllib.parse.urlparse(GATEWAY)
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=60)
        _thread_local.conn = conn
    return conn


def _reset_conn() -> None:
    conn = getattr(_thread_local, "conn", None)
    if conn is not None:
        try:
            conn.close()
        except Exception:
            pass
    _thread_local.conn = None


def percentile(values: list[float], p: float) -> float:
    if not values:
        return 0.0
    vals = sorted(values)
    idx = max(0, min(len(vals) - 1, int(p / 100.0 * (len(vals) - 1) + 0.5)))
    return vals[idx]


def classify_error(status: int, err: str | None) -> str:
    if err:
        low = err.lower()
        if "timeout" in low:
            return "timeout"
        if any(k in low for k in ("refused", "reset", "broken pipe", "connection")):
            return "connection_error"
        return "client_exception"
    if 200 <= status < 300:
        return "-"
    if status == 429:
        return "rate_limited_429"
    if status >= 500:
        return "server_error_5xx"
    if status == 413:
        return "payload_too_large"
    return f"http_{status}"


def chat_once(*, model: str, messages: list[dict], stream: bool = False,
              session_id: str | None = None, max_tokens: int = 64,
              timeout: float = 45.0) -> dict:
    """One chat completion through the gateway. Returns a result record."""
    payload = json.dumps({
        "model": model,
        "messages": messages,
        "max_tokens": max_tokens,
        "stream": stream,
    }).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {API_KEY}",
        "Accept": "text/event-stream" if stream else "application/json",
    }
    if session_id:
        headers["X-Gw-Session-Id"] = session_id

    rec: dict = {
        "ok": False, "status": 0, "latency_ms": 0.0, "ttfb_ms": None,
        "vendor": None, "stream": stream, "saw_done": False,
        "saw_envelope": False, "content_echo_ok": None,
        "error": None, "error_class": None, "bytes": 0,
    }
    started = time.monotonic()
    body_text_parts: list[str] = []
    try:
        conn = _conn()
        conn.timeout = timeout
        conn.request("POST", "/v1/chat/completions", body=payload, headers=headers)
        resp = conn.getresponse()
        rec["status"] = resp.status
        first_byte_at = None

        if stream:
            buf = b""
            saw_done = False
            saw_envelope = False
            while True:
                chunk = resp.read(8192)
                if not chunk:
                    break
                if first_byte_at is None:
                    first_byte_at = time.monotonic()
                    rec["ttfb_ms"] = (first_byte_at - started) * 1000.0
                rec["bytes"] += len(chunk)
                buf += chunk
                if b"data: [DONE]" in chunk:
                    saw_done = True
                if b"upstream_incomplete" in chunk or b"eof_without_done" in chunk:
                    saw_envelope = True
                if len(buf) > 4 * 1024 * 1024:
                    break
            rec["saw_done"] = saw_done
            rec["saw_envelope"] = saw_envelope
            body_text_parts.append(buf.decode("utf-8", errors="replace"))
        else:
            raw = resp.read()
            if raw:
                rec["bytes"] = len(raw)
                body_text_parts.append(raw.decode("utf-8", errors="replace"))
        rec["latency_ms"] = (time.monotonic() - started) * 1000.0
    except (socket.timeout, TimeoutError) as exc:
        rec["latency_ms"] = (time.monotonic() - started) * 1000.0
        rec["error"] = f"timeout: {exc}"[:160]
        rec["error_class"] = classify_error(0, rec["error"])
        _reset_conn()
        return rec
    except (ConnectionError, BrokenPipeError, http.client.HTTPException, OSError) as exc:
        rec["latency_ms"] = (time.monotonic() - started) * 1000.0
        rec["error"] = f"{type(exc).__name__}: {exc}"[:160]
        rec["error_class"] = classify_error(0, rec["error"])
        _reset_conn()
        return rec

    text = "".join(body_text_parts)
    vendor = None
    echo_ok = None
    if 200 <= rec["status"] < 300:
        m = ID_RE.search(text) or VENDOR_RE.search(text)
        if m:
            vendor = f"mock-vendor-{m.group(1)}"
        else:
            try:
                body = json.loads(text)
                vendor = body.get("_mock_identity")
                content = (body.get("choices") or [{}])[0].get("message", {}).get("content", "")
                if content:
                    m2 = VENDOR_RE.search(content)
                    if m2:
                        vendor = f"mock-vendor-{m2.group(1)}"
            except Exception:
                pass
        rec["ok"] = True
        if stream:
            rec["ok"] = rec["saw_done"] or rec["saw_envelope"]
    rec["vendor"] = vendor
    rec["error_class"] = classify_error(rec["status"], None)
    # content echo assertions are filled by callers that send a probe tag
    rec["_body_text"] = text[:4000]
    rec["content_echo_ok"] = echo_ok
    return rec


def chat_echo_ok(rec: dict, probe_tag: str) -> bool:
    """True when the reply echoed the probe tag (proves the caller's
    message reached the mock inside the request history)."""
    return probe_tag[:60] in rec.get("_body_text", "")


# ══════════════════════════════════════════════════════════════════════
# Mock control + gateway admin recovery
# ══════════════════════════════════════════════════════════════════════

def http_json(method: str, url: str, body: dict | None = None,
              headers: dict | None = None, timeout: float = 10.0):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(url, data=data, method=method,
                                 headers={"Content-Type": "application/json", **(headers or {})})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        raw = resp.read()
        return resp.status, (json.loads(raw) if raw else {})


def set_mock(name: str, mode: str, **extra) -> bool:
    payload = {"mode": mode, **extra}
    try:
        status, _ = http_json("POST", f"{MOCK_URLS[name]}/admin/state", payload)
        ok = status == 200
    except Exception as exc:
        log(f"  set_mock {name} → {mode} FAILED: {exc}")
        return False
    log(f"  mock-{name} → {mode}")
    return ok


def set_all_mocks(mode: str, **extra) -> bool:
    ok = True
    for name in MOCK_URLS:
        ok = set_mock(name, mode, **extra) and ok
    return ok


def mock_counter(name: str, key: str = "requests") -> int:
    try:
        _, body = http_json("GET", f"{MOCK_URLS[name]}/admin/stats")
        return int(body.get("counters", {}).get(key, 0))
    except Exception:
        return -1


def admin_login() -> str | None:
    try:
        _, body = http_json("POST", f"{GATEWAY}/api/auth/token",
                            {"username": ADMIN_USER, "password": ADMIN_PASSWORD})
        token = body.get("access_token") or body.get("token")
        return token or None
    except Exception as exc:
        log(f"  admin login failed: {exc}")
        return None


def admin_recover_all(token: str | None) -> dict:
    """Clear gateway-side cooldowns through the product's own admin API."""
    out = {"providers_reset": 0, "availability_reset": 0, "errors": []}
    if not token:
        out["errors"].append("no_admin_token")
        return out
    auth = {"Authorization": f"Bearer {token}"}
    for pid in VENDOR_IDS:
        try:
            http_json("POST", f"{GATEWAY}/api/providers/{pid}/node-probe-state/reset", {}, auth)
            out["providers_reset"] += 1
        except Exception as exc:
            out["errors"].append(f"probe-reset-{pid}: {exc}")
        try:
            _, creds = http_json("GET", f"{GATEWAY}/api/providers/{pid}/credentials", None, auth)
            cred_list = creds if isinstance(creds, list) else creds.get("credentials", creds.get("data", []))
            for cred in cred_list or []:
                cid = cred.get("id")
                if not cid:
                    continue
                for path in ("reset-availability", "recover"):
                    try:
                        http_json("POST", f"{GATEWAY}/api/providers/{pid}/credentials/{cid}/{path}", {}, auth)
                        out["availability_reset"] += 1
                    except Exception as exc:
                        out["errors"].append(f"{path}-{cid}: {exc}")
        except Exception as exc:
            out["errors"].append(f"creds-{pid}: {exc}")
    return out


def wait_recovery(min_streak: int = 15, min_vendors: int = 2,
                  max_wait: float = 90.0) -> tuple[bool, set[str]]:
    """Poll until min_streak consecutive successes span min_vendors vendors."""
    deadline = time.monotonic() + max_wait
    streak = 0
    vendors: set[str] = set()
    while time.monotonic() < deadline:
        rec = chat_once(model="mock-model-fast",
                        messages=[{"role": "user", "content": f"recovery-poll-{time.time_ns()}"}],
                        max_tokens=16, timeout=20)
        if rec["ok"] and rec["vendor"]:
            streak += 1
            vendors.add(rec["vendor"])
            if streak >= min_streak and len(vendors) >= min_vendors:
                return True, vendors
        else:
            streak = 0
            vendors = set()
        time.sleep(1.0)
    return False, vendors


# ══════════════════════════════════════════════════════════════════════
# Concurrent runner
# ══════════════════════════════════════════════════════════════════════

def run_concurrent(worker_fn, concurrency: int) -> list[dict]:
    results: list[dict] = []
    lock = threading.Lock()

    def wrapped(wid: int) -> None:
        local = worker_fn(wid)
        with lock:
            results.extend(local)

    threads = [threading.Thread(target=wrapped, args=(w,), daemon=True)
               for w in range(concurrency)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    return results


def summarize(records: list[dict], label: str) -> dict:
    total = len(records)
    ok = sum(1 for r in records if r["ok"])
    lat = [r["latency_ms"] for r in records]
    ttfb = [r["ttfb_ms"] for r in records if r["ttfb_ms"] is not None]
    return {
        "label": label,
        "total": total,
        "success": ok,
        "failed": total - ok,
        "success_rate": round(ok * 100.0 / total, 2) if total else 0.0,
        "latency_ms": {
            "p50": round(percentile(lat, 50), 1),
            "p95": round(percentile(lat, 95), 1),
            "p99": round(percentile(lat, 99), 1),
            "max": round(max(lat), 1) if lat else 0.0,
            "avg": round(statistics.mean(lat), 1) if lat else 0.0,
        },
        "ttfb_p95_ms": round(percentile(ttfb, 95), 1) if ttfb else None,
        "status_codes": dict(Counter(str(r["status"]) for r in records)),
        "vendors": dict(Counter(r["vendor"] or "unknown" for r in records if r["ok"])),
        "error_classes": dict(Counter(r["error_class"] for r in records if not r["ok"])),
    }


def record(name: str, passed: bool, details: dict, notes: str = "") -> None:
    SCENARIOS.append({"scenario": name, "passed": passed, "details": details, "notes": notes})
    log(f"  ⇒ {name}: {'✅ PASS' if passed else '❌ FAIL'}")


def burst(total: int, concurrency: int, **kwargs) -> list[dict]:
    """Fire `total` requests across `concurrency` workers."""
    per = [total // concurrency] * concurrency
    for i in range(total % concurrency):
        per[i] += 1

    def worker(wid: int) -> list[dict]:
        out = []
        for _ in range(per[wid]):
            out.append(chat_once(**kwargs))
        return out

    return run_concurrent(worker, concurrency)


# ══════════════════════════════════════════════════════════════════════
# Scenarios
# ══════════════════════════════════════════════════════════════════════

def reset_clean(token: str | None) -> dict:
    set_all_mocks("healthy")
    rec = admin_recover_all(token)
    ok, vendors = wait_recovery(min_streak=10, min_vendors=2, max_wait=60)
    rec["e2e_recovered"] = ok
    rec["vendors_seen"] = sorted(vendors)
    return rec


def scenario_s01_baseline(quick: bool) -> None:
    log("── S01 baseline (all healthy) ──")
    token = admin_login()
    clean = reset_clean(token)
    total, conc = (100, 20) if quick else (200, 20)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s1-baseline"}], max_tokens=32)
    s = summarize(recs, "baseline")
    passed = (s["success_rate"] >= 99.0 and s["latency_ms"]["p95"] < 5000
              and len(s["vendors"]) >= 2 and clean["e2e_recovered"])
    record("S01_baseline", passed, {"summary": s, "reset": clean},
           notes="全 healthy：成功率≥99%，p95<5s，≥2 个 mock 供应商被命中，管理端恢复通道可用")


def scenario_s02_single_5xx(quick: bool) -> None:
    log("── S02 single vendor 5xx failover ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "server_error")
    time.sleep(1)
    total, conc = (60, 10) if quick else (120, 12)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s2-failover"}], max_tokens=32)
    s = summarize(recs, "alpha_500")
    alpha_hits = s["vendors"].get("mock-vendor-alpha", 0)
    passed = s["success_rate"] >= 90.0 and alpha_hits <= total * 0.05
    record("S02_single_5xx_failover", passed,
           {"summary": s, "alpha_success_hits": alpha_hits},
           notes="alpha 恒 500：成功率≥90% 且成功响应里 alpha 占比≤5%（failover 生效）")
    set_mock("alpha", "healthy")


def scenario_s03_consecutive_demotion(quick: bool) -> None:
    log("── S03 consecutive 5xx demotion ──")
    token = admin_login()
    reset_clean(token)
    before = mock_counter("alpha")
    set_mock("alpha", "server_error")
    time.sleep(1)
    # warm-up burst drives consecutive failures; then a second window
    total1 = 40 if quick else 60
    burst(total1, 10, model="mock-model-fast",
          messages=[{"role": "user", "content": "s3-demotion"}], max_tokens=16)
    time.sleep(2)
    total2 = 40 if quick else 60
    recs = burst(total2, 10, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s3-demotion"}], max_tokens=16)
    after = mock_counter("alpha")
    s = summarize(recs, "after_demotion")
    alpha_second_window = after - before - total1
    passed = (s["success_rate"] >= 90.0 and alpha_second_window < total2 * 0.3)
    record("S03_consecutive_5xx_demotion", passed,
           {"summary": s, "alpha_requests_window1": total1, "alpha_requests_window2": alpha_second_window},
           notes="连续 5xx 后第二窗口 alpha 上游请求数骤降（降级生效），整体成功率≥90%")
    set_mock("alpha", "healthy")


def scenario_s04_slow_vendor(quick: bool) -> None:
    log("── S04 slow vendor ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "slow")
    time.sleep(1)
    total, conc = (30, 8) if quick else (60, 10)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s4-slow"}], max_tokens=16, timeout=60)
    s = summarize(recs, "alpha_slow")
    alpha_share = s["vendors"].get("mock-vendor-alpha", 0) / max(1, s["success"])
    passed = s["success_rate"] >= 95.0 and alpha_share < 0.5
    record("S04_slow_vendor", passed, {"summary": s, "alpha_success_share": round(alpha_share, 3)},
           notes="alpha 5-30s 慢响应：成功率≥95% 且成功流量中 alpha 占比<50%（慢节点被规避）")
    set_mock("alpha", "healthy")


def scenario_s05_rate_limit(quick: bool) -> None:
    log("── S05 rate limit 429 ──")
    token = admin_login()
    reset_clean(token)
    before_429 = mock_counter("alpha", "rate_limited")
    set_mock("alpha", "rate_limited")
    time.sleep(1)
    total, conc = (60, 10) if quick else (120, 12)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s5-ratelimit"}], max_tokens=32)
    after_429 = mock_counter("alpha", "rate_limited")
    s = summarize(recs, "alpha_429")
    passed = (s["success_rate"] >= 90.0 and after_429 > before_429)
    record("S05_rate_limit_429", passed,
           {"summary": s, "alpha_429_served": after_429 - before_429},
           notes="alpha 恒 429：上游确实吃到了 429（计数>0）且整体成功率≥90%（限流被网关吸收）")
    set_mock("alpha", "healthy")


def scenario_s06_all_down(quick: bool) -> None:
    log("── S06 all vendors down — graceful 503 ──")
    admin_login()
    set_all_mocks("no_available")
    time.sleep(1)
    total, conc = (8, 4) if quick else (15, 5)
    started = time.monotonic()
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s6-alldown"}], max_tokens=16, timeout=60)
    wall = time.monotonic() - started
    s = summarize(recs, "all_down")
    no_fake_success = all(not (r["ok"] and not r["_body_text"].strip()) for r in recs)
    statuses_ok = all(r["status"] >= 500 or r["error_class"] in ("server_error_5xx", "timeout", "connection_error") for r in recs)
    passed = (s["success_rate"] == 0.0 and wall < 90.0 and no_fake_success and statuses_ok)
    record("S06_all_down_graceful", passed, {"summary": s, "wall_s": round(wall, 1)},
           notes="全故障：0% 成功（不伪成功）、快速失败（<90s，无挂起）、状态全部为 5xx/超时族")
    set_all_mocks("healthy")


def scenario_s07_recovery(quick: bool) -> None:
    log("── S07 recovery & traffic return ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "server_error")
    time.sleep(1)
    burst(60, 10, model="mock-model-fast",
          messages=[{"role": "user", "content": "s7-pre"}], max_tokens=16)
    set_mock("alpha", "healthy")
    rec = admin_recover_all(token)
    ok, vendors = wait_recovery(min_streak=20, min_vendors=3, max_wait=120)
    total, conc = (50, 10) if quick else (100, 10)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s7-post"}], max_tokens=32)
    s = summarize(recs, "post_recovery")
    alpha_back = s["vendors"].get("mock-vendor-alpha", 0) > 0
    passed = ok and s["success_rate"] >= 99.0 and alpha_back and len(vendors) >= 3
    record("S07_recovery", passed,
           {"summary": s, "recovery_poll": {"ok": ok, "vendors": sorted(vendors)}, "admin_recover": rec},
           notes="故障解除后：轮询恢复（≥3 供应商）+ 后续成功率≥99% + alpha 重新接流（恢复被证实，不是被永久拉黑）")


def scenario_s08_stream_integrity(quick: bool) -> None:
    log("── S08 stream integrity ──")
    token = admin_login()
    reset_clean(token)
    total, conc = (40, 10) if quick else (100, 20)

    def worker_fn() -> list[dict]:
        def gen(wid: int) -> list[dict]:
            return []
        return gen

    per = [total // conc] * conc
    for i in range(total % conc):
        per[i] += 1

    def worker(wid: int) -> list[dict]:
        out = []
        for _ in range(per[wid]):
            out.append(chat_once(model="mock-model-stream",
                                 messages=[{"role": "user", "content": "s8-stream"}],
                                 stream=True, max_tokens=48, timeout=60))
        return out

    recs = run_concurrent(worker, conc)
    s = summarize(recs, "stream")
    done_rate = sum(1 for r in recs if r["saw_done"]) / max(1, total)
    passed = s["success_rate"] >= 99.0 and done_rate >= 0.99
    record("S08_stream_integrity", passed,
           {"summary": s, "done_rate": round(done_rate, 4)},
           notes="SSE：成功率≥99% 且 [DONE] 完整率≥99%（无静默截断）")


def scenario_s09_broken_stream(quick: bool) -> None:
    log("── S09 broken stream (committed EOF) ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "broken_stream")
    set_mock("beta", "broken_stream")
    time.sleep(1)
    total = 20 if quick else 40
    recs = burst(total, 8, model="mock-model-stream",
                 messages=[{"role": "user", "content": "s9-broken"}],
                 stream=True, max_tokens=32, timeout=60)
    s = summarize(recs, "broken_stream")
    terminated = sum(1 for r in recs if r["saw_done"] or r["saw_envelope"])
    envelopes = sum(1 for r in recs if r["saw_envelope"])
    healthy_failovers = sum(1 for r in recs if r["ok"] and r["vendor"] in ("mock-vendor-gamma", "mock-vendor-delta"))
    truncated_silent = sum(1 for r in recs if not r["saw_done"] and not r["saw_envelope"] and r["status"] == 200)
    passed = (terminated == total and truncated_silent == 0
              and (envelopes > 0 or healthy_failovers > 0))
    record("S09_broken_stream", passed,
           {"summary": s, "terminated": terminated, "envelopes": envelopes,
            "healthy_failovers": healthy_failovers, "silent_truncations": truncated_silent},
           notes="断流：每条响应都被终止（[DONE] 或 upstream_incomplete 信封），零静默截断；信封与 failover 两路至少一路出现")
    set_mock("alpha", "healthy")
    set_mock("beta", "healthy")


def scenario_s10_flaky(quick: bool) -> None:
    log("── S10 flaky vendor ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "flaky")
    time.sleep(1)
    total, conc = (80, 16) if quick else (200, 20)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s10-flaky"}], max_tokens=32, timeout=60)
    s = summarize(recs, "flaky")
    passed = s["success_rate"] >= 85.0
    record("S10_flaky_vendor", passed, {"summary": s},
           notes="alpha 50% 抖动失败：整体成功率≥85%（重试+failover 吸收）")
    set_mock("alpha", "healthy")


def scenario_s11_chaos_mixed(quick: bool) -> None:
    log("── S11 chaos: mixed fault modes ──")
    token = admin_login()
    reset_clean(token)
    set_mock("alpha", "server_error")
    set_mock("beta", "slow")
    set_mock("gamma", "flaky")
    set_mock("delta", "healthy")
    time.sleep(1)
    total, conc = (60, 12) if quick else (150, 15)
    recs = burst(total, conc, model="mock-model-fast",
                 messages=[{"role": "user", "content": "s11-chaos"}], max_tokens=32, timeout=60)
    s = summarize(recs, "chaos")
    delta_share = s["vendors"].get("mock-vendor-delta", 0) / max(1, s["success"])
    passed = s["success_rate"] >= 80.0 and delta_share >= 0.3
    record("S11_chaos_mixed", passed,
           {"summary": s, "delta_success_share": round(delta_share, 3)},
           notes="混合故障（500/慢/抖动/健康并存）：成功率≥80%，健康节点 delta 承接≥30% 成功流量")
    set_all_mocks("healthy")


def scenario_s12_burst_high_concurrency(quick: bool) -> None:
    log("── S12 high-concurrency burst ──")
    token = admin_login()
    reset_clean(token)
    if quick:
        total, conc = (400, 50)
    else:
        total, conc = (2000, 100)
    long_prompt = "context-token-" * 400  # ~5.6KB per request for the long model

    def make_payload(i: int) -> dict:
        if i % 4 == 3:
            return dict(model="mock-model-long",
                        messages=[{"role": "user", "content": f"s12-long-{i} {long_prompt}"}],
                        max_tokens=64, timeout=60)
        return dict(model="mock-model-fast",
                    messages=[{"role": "user", "content": f"s12-fast-{i}"}],
                    max_tokens=32, timeout=60)

    payloads = [make_payload(i) for i in range(total)]
    idx = {"n": 0}
    lock = threading.Lock()
    results: list[dict] = []

    def worker(wid: int) -> None:
        while True:
            with lock:
                i = idx["n"]
                idx["n"] += 1
            if i >= total:
                return
            results.append(chat_once(**payloads[i]))

    started = time.monotonic()
    run_concurrent(worker, conc)
    wall = time.monotonic() - started
    s = summarize(results, "burst")
    rps = total / max(wall, 0.001)
    vendors_all = len(s["vendors"]) >= 4
    passed = (s["success_rate"] >= 99.0 and s["latency_ms"]["p95"] < 5000 and vendors_all)
    record("S12_burst_high_concurrency", passed,
           {"summary": s, "concurrency": conc, "wall_s": round(wall, 1), "rps": round(rps, 1)},
           notes=f"高压突发 {total}@c{conc}（25% 长上下文 ~5.6KB）：成功率≥99%，p95<5s，4 家 mock 全部命中")


def scenario_s13_long_session(quick: bool) -> None:
    log("── S13 long multi-turn sessions ──")
    token = admin_login()
    reset_clean(token)
    sessions_n = 4 if quick else 10
    rounds = 10 if quick else 25
    fault_at_round = max(2, rounds // 3)
    heal_at_round = max(4, rounds * 2 // 3)

    sessions = [f"mock-stress-sess-{i:03d}-{int(time.time())}" for i in range(sessions_n)]
    histories: dict[str, list[dict]] = {sid: [] for sid in sessions}
    results: list[dict] = []
    state = {"alpha_faulted": False, "healed": True}
    slock = threading.Lock()

    def round_tag(sid: str, r: int) -> str:
        return f"p{int(time.time()*1000)%100000000000}{r:02d}{os.urandom(3).hex()}"

    def run_session(worker_id: int) -> None:
        sid = sessions[worker_id % len(sessions)]
        for r in range(1, rounds + 1):
            with slock:
                if r == fault_at_round and not state["alpha_faulted"]:
                    state["alpha_faulted"] = True
                    set_mock("alpha", "server_error")
                if r == heal_at_round and state["alpha_faulted"]:
                    state["healed"] = False
                    set_mock("alpha", "healthy")
            tag = round_tag(sid, r)
            hist = histories[sid]
            hist.append({"role": "user", "content": tag})
            rec = chat_once(model="mock-model-fast", messages=list(hist),
                            session_id=sid, max_tokens=64, timeout=60)
            rec["session"] = sid
            rec["round"] = r
            rec["probe_tag"] = tag
            rec["echo_ok"] = chat_echo_ok(rec, tag)
            # only keep successful assistant turns in history (client-SDK semantics)
            if rec["ok"] and rec["echo_ok"]:
                hist.append({"role": "assistant", "content": rec["_body_text"][:200]})
            results.append(rec)

    run_concurrent(run_session, sessions_n)
    s = summarize(results, "long_session")
    total = len(results)
    echo_rate = sum(1 for r in results if r["echo_ok"]) / max(1, total)
    by_session: dict[str, dict] = {}
    for r in results:
        agg = by_session.setdefault(r["session"], {"rounds": 0, "ok": 0, "vendors": Counter(), "first_ok_vendor": None})
        agg["rounds"] += 1
        if r["ok"]:
            agg["ok"] += 1
            agg["vendors"][r["vendor"] or "unknown"] += 1
            if agg["first_ok_vendor"] is None:
                agg["first_ok_vendor"] = r["vendor"]
    completed = sum(1 for agg in by_session.values() if agg["rounds"] == rounds)
    during_fault = [r for r in results if fault_at_round <= r["round"] < heal_at_round]
    fault_s = summarize(during_fault, "during_fault") if during_fault else {"success_rate": 100.0}
    passed = (s["success_rate"] >= 95.0 and completed == sessions_n
              and echo_rate >= 0.95 and fault_s["success_rate"] >= 60.0)
    record("S13_long_session_multiturn", passed, {
        "summary": s,
        "echo_rate": round(echo_rate, 4),
        "sessions_completed": f"{completed}/{sessions_n}",
        "sessions_detail": {
            sid: {"ok": agg["ok"], "rounds": agg["rounds"], "vendors": dict(agg["vendors"])}
            for sid, agg in by_session.items()
        },
        "during_fault_window": {k: v for k, v in fault_s.items() if k != "vendors"},
        "fault_round": fault_at_round, "heal_round": heal_at_round,
    }, notes=(f"{sessions_n} 会话 × {rounds} 轮真实多轮（历史全量上送，会话头粘性）："
              "整体成功率≥95%，全部会话跑满轮数，上下文回显率≥95%（历史确实到达上游），"
              f"故障窗口内（第 {fault_at_round} 轮注入 alpha 500）成功率≥60%，其余流量 failover 兜住"))


def scenario_s14_soak(quick: bool) -> None:
    log("── S14 sustained soak + leak watch ──")
    token = admin_login()
    reset_clean(token)
    duration = 30 if quick else 120
    conc = 20 if quick else 50
    started = time.monotonic()
    deadline = started + duration
    results: list[dict] = []
    lock = threading.Lock()
    counter = {"n": 0}

    def worker(wid: int) -> None:
        sid = f"soak-{wid:03d}"
        while time.monotonic() < deadline:
            with lock:
                i = counter["n"]
                counter["n"] += 1
            stream = (i % 5 == 4)
            rec = chat_once(model="mock-model-stream" if stream else "mock-model-fast",
                            messages=[{"role": "user", "content": f"soak-{i}"}],
                            stream=stream, session_id=sid,
                            max_tokens=48 if stream else 32, timeout=60)
            with lock:
                results.append(rec)

    run_concurrent(worker, conc)
    wall = time.monotonic() - started
    s = summarize(results, "soak")
    rps = len(results) / max(wall, 0.001)
    try:
        status, health = http_json("GET", f"{GATEWAY}/healthz", timeout=5)
        gw_ok = status == 200
    except Exception as exc:
        gw_ok = False
        health = {"error": str(exc)}
    passed = s["success_rate"] >= 99.0 and gw_ok
    record("S14_sustained_soak", passed,
           {"summary": s, "duration_s": round(wall, 1), "concurrency": conc,
            "rps": round(rps, 1), "gateway_health_after": health},
           notes=f"持续 {duration}s @ c{conc} 混合流式/非流式：成功率≥99%，结束后 /healthz 仍健康")


SCENARIO_IMPL = {
    "S01": scenario_s01_baseline,
    "S02": scenario_s02_single_5xx,
    "S03": scenario_s03_consecutive_demotion,
    "S04": scenario_s04_slow_vendor,
    "S05": scenario_s05_rate_limit,
    "S06": scenario_s06_all_down,
    "S07": scenario_s07_recovery,
    "S08": scenario_s08_stream_integrity,
    "S09": scenario_s09_broken_stream,
    "S10": scenario_s10_flaky,
    "S11": scenario_s11_chaos_mixed,
    "S12": scenario_s12_burst_high_concurrency,
    "S13": scenario_s13_long_session,
    "S14": scenario_s14_soak,
}


# ══════════════════════════════════════════════════════════════════════
# preflight + report
# ══════════════════════════════════════════════════════════════════════

def preflight() -> tuple[bool, dict]:
    info: dict = {"gateway": GATEWAY}
    try:
        status, health = http_json("GET", f"{GATEWAY}/healthz", timeout=5)
        info["gateway_healthz"] = health if status == 200 else f"http-{status}"
    except Exception as exc:
        info["gateway_healthz"] = f"error: {exc}"
        return False, info
    for name, base in MOCK_URLS.items():
        try:
            _, body = http_json("GET", f"{base}/healthz", timeout=5)
            info[f"mock_{name}"] = body.get("mode", "?")
        except Exception as exc:
            info[f"mock_{name}"] = f"error: {exc}"
            return False, info
    rec = chat_once(model="mock-model-fast",
                    messages=[{"role": "user", "content": "preflight"}], max_tokens=16, timeout=30)
    info["e2e_smoke"] = {"ok": rec["ok"], "vendor": rec["vendor"], "status": rec["status"],
                         "error": rec["error"]}
    if not rec["ok"]:
        log(f"  E2E smoke FAILED: status={rec['status']} err={rec['error']}")
        log(f"  body head: {rec.get('_body_text', '')[:300]}")
    return bool(rec["ok"]), info


def write_report(env_info: dict, migration_ledger: str = "") -> Path:
    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    total_pass = sum(1 for r in SCENARIOS if r["passed"])
    md = ["# Mock-Only Gateway 压测报告（生产 cmd/gateway + 假想供应商）", ""]
    md.append(f"**时间**: {env_info.get('timestamp', '')}  ")
    md.append(f"**网关**: `{GATEWAY}`  ")
    md.append(f"**平台**: {env_info.get('platform', '')}  ")
    md.append("")
    md.append("| 场景 | 结果 |")
    md.append("|------|------|")
    for r in SCENARIOS:
        md.append(f"| {r['scenario']} | {'✅ 通过' if r['passed'] else '❌ 失败'} |")
    md.append(f"| **合计** | **{total_pass}/{len(SCENARIOS)} 通过** |")
    md.append("")
    if migration_ledger:
        md.append("## PG 初始化迁移失败台账（非致命，如实记录）")
        md.append("")
        md.append("```text")
        md.append(migration_ledger.strip() or "(无)")
        md.append("```")
        md.append("")
    for r in SCENARIOS:
        md.append(f"## {r['scenario']}")
        md.append("")
        if r["notes"]:
            md.append(f"**判定标准**: {r['notes']}")
            md.append("")
        md.append("```json")
        md.append(json.dumps(r["details"], ensure_ascii=False, indent=2))
        md.append("```")
        md.append("")
    md_path = REPORT_DIR / f"mock-only-{ts}.md"
    md_path.write_text("\n".join(md), encoding="utf-8")
    json_path = REPORT_DIR / f"mock-only-{ts}.json"
    json_path.write_text(json.dumps({"env": env_info, "results": SCENARIOS},
                                    ensure_ascii=False, indent=2), encoding="utf-8")
    # stable-name copy for tooling
    (REPORT_DIR / "latest.json").write_text(
        json.dumps({"env": env_info, "results": SCENARIOS}, ensure_ascii=False, indent=2),
        encoding="utf-8")
    return md_path


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--scenarios", default=",".join(SCENARIO_IMPL),
                        help="comma list like S01,S12,S13")
    parser.add_argument("--quick", action="store_true", help="reduced counts for iteration")
    args = parser.parse_args()

    selected = [s.strip().upper() for s in args.scenarios.split(",") if s.strip()]
    for s in selected:
        if s not in SCENARIO_IMPL:
            print(f"unknown scenario: {s}; valid: {','.join(SCENARIO_IMPL)}", file=sys.stderr)
            return 2

    import platform
    env_info = {
        "timestamp": datetime.now(timezone.utc).isoformat(),
        "gateway": GATEWAY,
        "platform": f"{platform.system()} {platform.machine()} py{platform.python_version()}",
    }

    log("── preflight ──")
    ok, info = preflight()
    env_info["preflight"] = info
    if not ok:
        log("preflight failed — aborting (see preflight info above)")
        write_report(env_info)
        return 2
    log(f"  preflight ok: {json.dumps(info, ensure_ascii=False)}")

    ledger = ""
    ledger_path = Path("/tmp/mock-only-migration-failures.txt")
    if ledger_path.exists():
        ledger = ledger_path.read_text(errors="replace")
    env_info["migration_failures"] = len([l for l in ledger.splitlines() if l.startswith("=== ")])
    if ledger:
        log(f"  migration failure ledger: {env_info['migration_failures']} entries (recorded in report)")

    for sid in selected:
        SCENARIO_IMPL[sid](args.quick)
        set_all_mocks("healthy")
        time.sleep(2)

    report = write_report(env_info, ledger)
    passed_n = sum(1 for r in SCENARIOS if r["passed"])
    log(f"报告: {report}")
    log(f"测试完成: {passed_n}/{len(SCENARIOS)} 通过")
    return 0 if passed_n == len(SCENARIOS) else 1


if __name__ == "__main__":
    sys.exit(main())
