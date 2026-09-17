#!/usr/bin/env python3
"""Mock-client E2E for the no-available-nodes retry path (product spec).

When a client requests a model whose nodes are all unavailable, the gateway
must (product spec):

  (a) hold the SSE connection open,
  (b) emit one `: thinking:` chunk per retry (~30s apart under request
      survival; the legacy pre-stream retry uses exponential 2s..120s),
  (c) stop gracefully after the retry budget or the 5h deadline,
  (d) stop immediately when the client disconnects.

IMPORTANT — what this script can and cannot verify (be honest):

  * The SurvivalCoordinator (request survival: 100 day / 600 night / 30s
    fixed cadence / 5h deadline / disconnect-stop) is DEFAULT-OFF
    (`request_survival_enabled: false`). Against a default local deploy this
    script observes the LEGACY pre-stream retry path instead — its think
    message reads "上游请求暂时失败（…），正在重试…" and the cadence is
    exponential, not 30s. Pass --interval accordingly (e.g. 6 ± tolerance 8)
    for that path, or enable survival first.
  * The authoritative verification of the survival path — including (c)
    budget/deadline termination and (d) server-side stop-on-disconnect — is
    the Go E2E suite `domains/streaming/survival_no_nodes_e2e_test.go`, which
    drives the real ChatHandler + SurvivalCoordinator over a real HTTP
    socket. This script is the live-gateway smoke driver; scenario (d) here
    only proves the client-side drop is clean, it cannot observe the
    server-side loop. Scenario (c) is NOT driven live (100×30s is not a
    practical wait).

Usage:
    python3 tests/mock-system-test/scenario_no_nodes_survival.py --model glm-5.2
    python3 tests/mock-system-test/scenario_no_nodes_survival.py --model gpt-5.6-luna --interval 6 --tolerance 8

Output:
    tests/mock-system-test/reports/no-nodes-<ts>.json / .md
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import urllib3
from urllib3 import PoolManager

REPORT_DIR = Path(__file__).resolve().parent / "reports"
DEFAULT_GATEWAY = os.environ.get("LLM_GATEWAY_URL", "http://127.0.0.1:8782")
DEFAULT_API_KEY = os.environ.get("LLM_GATEWAY_API_KEY", "sk-e2e-test-1781898294")


@dataclass
class ThinkEvent:
    matched: bool          # True when the chunk was a `: thinking:` comment
    attempt: int | None    # "第 N 次" if present
    reason: str | None     # "原因=xxx" (survival form) if present
    wait_seconds: int | None
    message: str           # raw decoded payload text
    received_at: float


@dataclass
class RunResult:
    scenario: str
    model: str
    started_at: str
    ended_at: str
    duration_sec: float
    http_status: int | None
    connection_alive_at_end: bool
    thinks: list[ThinkEvent] = field(default_factory=list)
    started_mono: float = 0.0
    first_data_at: float | None = None
    last_byte_at: float | None = None
    error: str | None = None
    notes: list[str] = field(default_factory=list)


def log(msg: str) -> None:
    print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] {msg}", flush=True)


# ---------------------------------------------------------------------------
# wire helpers
# ---------------------------------------------------------------------------


def open_stream(
    *,
    gateway: str,
    api_key: str,
    model: str,
    interval: float,
) -> tuple[urllib3.response.HTTPResponse | None, str, float]:
    """POST one streaming chat completion; returns (resp, err, started_mono)."""
    body = json.dumps(
        {
            "model": model,
            "stream": True,
            "messages": [{"role": "user", "content": "ping"}],
        }
    ).encode("utf-8")

    http = PoolManager(retries=False, timeout=urllib3.Timeout(connect=5.0, read=interval + 60))
    started_mono = time.monotonic()
    try:
        resp = http.urlopen(
            "POST",
            f"{gateway}/v1/chat/completions",
            body=body,
            headers={
                "Content-Type": "application/json",
                "Authorization": f"Bearer {api_key}",
                "Accept": "text/event-stream",
            },
            preload_content=False,
        )
    except urllib3.exceptions.MaxRetryError as e:
        return None, str(e), started_mono
    return resp, "ok", started_mono


# Matches the production `: thinking: "..."` SSE comment. Two producers exist:
#   - SurvivalCoordinator path (survival_wiring.go):
#       "正在等待可用节点并重试（第 N 次，原因=<reason>，等待 Xs）"
#   - Legacy pre-stream retry (handler.go writeThinking):
#       "上游请求暂时失败（<kind>），正在重试...（等待 Xs 后第 N 次重试）"
THINK_RE = re.compile(
    rb"^: thinking:\s*(?P<msg>[^\n]+)\s*$",
    re.MULTILINE,
)
ATTEMPT_RE = re.compile("第\\s*(\\d+)\\s*次".encode("utf-8"))
WAIT_RE = re.compile("等待\\s*(\\d+)s".encode("utf-8"))
REASON_RE = re.compile("原因=([^，,）]+)".encode("utf-8"))
# Pre-stream exhausted-candidates terminal frame (survival OFF / flag-off path):
# data: {"error":{"message":"No available provider for model 'X'. All N candidates failed.","code":"model_not_found"}}
NO_CANDIDATES_RE = re.compile(rb"No available provider for model|candidates? failed")


def parse_think_chunk(chunk: bytes, received_at: float) -> ThinkEvent:
    m = THINK_RE.search(chunk)
    if not m:
        return ThinkEvent(False, None, None, None, "", received_at)
    msg = m.group("msg")
    decoded = msg.decode("utf-8", errors="replace").strip()
    # The payload is JSON-encoded (json.Marshal of the message string).
    try:
        text = json.loads(decoded)
        if not isinstance(text, str):
            text = decoded
    except json.JSONDecodeError:
        text = decoded
    encoded = text.encode("utf-8")

    attempt = None
    am = ATTEMPT_RE.search(encoded)
    if am:
        attempt = int(am.group(1))
    wait_seconds = None
    wm = WAIT_RE.search(encoded)
    if wm:
        wait_seconds = int(wm.group(1))
    reason = None
    rm = REASON_RE.search(encoded)
    if rm:
        reason = rm.group(1).decode("utf-8")
    return ThinkEvent(True, attempt, reason, wait_seconds, text, received_at)


def sse_frames(buf: bytearray) -> tuple[list[bytes], bytearray]:
    """Split complete SSE frames (terminated by a blank line) out of buf."""
    frames: list[bytes] = []
    while True:
        idx = bytes(buf).find(b"\n\n")
        if idx < 0:
            break
        frames.append(bytes(buf)[:idx])
        del buf[: idx + 2]
    return frames, buf


# ---------------------------------------------------------------------------
# scenario A(+B): connection stays open, thinks stream in, measure cadence
# ---------------------------------------------------------------------------


def run_thinks(
    *,
    gateway: str,
    api_key: str,
    model: str,
    interval: float,
    max_thinks: int,
    report: RunResult,
) -> RunResult:
    log(f"A: streaming model={model} max_thinks={max_thinks} expected_interval={interval}s")
    resp, err, started_mono = open_stream(gateway=gateway, api_key=api_key, model=model, interval=interval)
    report.started_mono = started_mono
    report.http_status = resp.status if resp is not None else None
    if resp is None or resp.status != 200:
        report.error = f"open_stream: {err}"
        report.ended_at = datetime.now(timezone.utc).isoformat()
        report.duration_sec = time.monotonic() - started_mono
        return report

    byte_buf = bytearray()
    last_byte_at = time.monotonic()
    saw_no_candidates_terminal = False
    try:
        for chunk in resp.stream(amt=4096):
            if not chunk:
                continue
            now = time.monotonic()
            byte_buf.extend(chunk)
            last_byte_at = now
            if report.first_data_at is None:
                report.first_data_at = now
            if NO_CANDIDATES_RE.search(chunk):
                saw_no_candidates_terminal = True
            frames, byte_buf = sse_frames(byte_buf)
            for raw in frames:
                ev = parse_think_chunk(raw, now)
                if not ev.matched:
                    continue
                report.thinks.append(ev)
                log(f"  think #{len(report.thinks)}: attempt={ev.attempt} reason={ev.reason} "
                    f"wait={ev.wait_seconds}s msg={ev.message[:48]!r}")
                if max_thinks and len(report.thinks) >= max_thinks:
                    log(f"  reached max_thinks={max_thinks}; closing stream")
                    raise StopIteration
    except StopIteration:
        pass
    except urllib3.exceptions.ProtocolError as e:
        report.notes.append(f"protocol_error={e!r}")
        log(f"  protocol error (treated as stream-end): {e}")
    except Exception as e:
        report.error = f"stream error: {e}"
        log(f"  stream error: {e}")
    finally:
        try:
            resp.close()
        except Exception:
            pass

    report.last_byte_at = last_byte_at
    report.connection_alive_at_end = (time.monotonic() - last_byte_at) < 5.0
    report.ended_at = datetime.now(timezone.utc).isoformat()
    report.duration_sec = time.monotonic() - started_mono
    if saw_no_candidates_terminal:
        report.notes.append(
            "observed exhausted-candidates terminal error frame (survival OFF behavior: "
            "the gateway errors immediately instead of holding the connection). Enable "
            "request_survival_enabled to exercise the hold-and-retry path.")
    return report


# ---------------------------------------------------------------------------
# scenario D: client disconnect — client-side evidence only (see module doc)
# ---------------------------------------------------------------------------


def run_disconnect(
    *,
    gateway: str,
    api_key: str,
    model: str,
    interval: float,
    wait_for_thinks: int,
    report: RunResult,
) -> RunResult:
    log(f"D: connect, read {wait_for_thinks} think(s), then drop the socket")
    resp, err, started_mono = open_stream(gateway=gateway, api_key=api_key, model=model, interval=interval)
    report.started_mono = started_mono
    report.http_status = resp.status if resp is not None else None
    if resp is None or resp.status != 200:
        report.error = f"open_stream: {err}"
        report.ended_at = datetime.now(timezone.utc).isoformat()
        report.duration_sec = time.monotonic() - started_mono
        return report

    byte_buf = bytearray()
    first_data_at = None
    last_byte_at = time.monotonic()
    dropped_at: float | None = None
    try:
        for chunk in resp.stream(amt=4096):
            if not chunk:
                continue
            now = time.monotonic()
            last_byte_at = now
            if first_data_at is None:
                first_data_at = now
            byte_buf.extend(chunk)
            frames, byte_buf = sse_frames(byte_buf)
            for raw in frames:
                ev = parse_think_chunk(raw, now)
                if not ev.matched:
                    continue
                report.thinks.append(ev)
                log(f"  think #{len(report.thinks)}: attempt={ev.attempt} reason={ev.reason}")
                if len(report.thinks) >= wait_for_thinks:
                    log("  disconnecting client socket now")
                    dropped_at = now
                    break
            if dropped_at is not None:
                break
    except urllib3.exceptions.ProtocolError as e:
        report.notes.append(f"protocol_error_during_read={e!r}")
        log(f"  protocol error (treated as stream-end): {e}")
    finally:
        try:
            resp.close()
        except Exception:
            pass

    if dropped_at is not None:
        time.sleep(2.0)
        report.notes.append(f"dropped_at={dropped_at - started_mono:.1f}s")
        report.notes.append(f"thinks_before_disconnect={len(report.thinks)}")
        log(f"  socket dropped at +{dropped_at - started_mono:.1f}s after {len(report.thinks)} think(s); "
            "server-side stop is verified by the Go E2E suite, not observable from here")

    report.first_data_at = first_data_at
    report.last_byte_at = last_byte_at
    report.ended_at = datetime.now(timezone.utc).isoformat()
    report.duration_sec = time.monotonic() - started_mono
    return report


# ---------------------------------------------------------------------------
# assertions
# ---------------------------------------------------------------------------


def assert_a_connection_stays_open(r: RunResult, errors: list[str]) -> None:
    if r.http_status != 200:
        errors.append(f"[A] expected HTTP 200, got {r.http_status}: {r.error}")
        return
    if r.thinks:
        log(f"[A] PASS: HTTP 200 held for {r.duration_sec:.1f}s, {len(r.thinks)} think chunk(s)")
        return
    if any("exhausted-candidates terminal" in n for n in r.notes):
        errors.append("[A] model currently has ZERO available candidates and request survival is OFF "
                      "on this gateway — it returned the immediate error frame instead of holding the "
                      "connection. This is the pre-spec behavior; enable request_survival_enabled to "
                      "exercise the hold-and-retry path this script is meant to observe.")
        return
    errors.append("[A] connection stayed open but no `: thinking:` chunk observed — "
                  "the model likely answered successfully (healthy upstream streams content, not thinks); "
                  "pick a failing model or retry while its credentials are degraded")


def assert_b_think_cadence(r: RunResult, expected_interval: float, tolerance: float, errors: list[str]) -> None:
    if len(r.thinks) < 2:
        errors.append(f"[B] need ≥2 thinks to measure cadence; got {len(r.thinks)}")
        return
    intervals = [curr.received_at - prev.received_at for prev, curr in zip(r.thinks, r.thinks[1:])]
    avg = sum(intervals) / len(intervals)
    if abs(avg - expected_interval) > tolerance:
        errors.append(f"[B] avg think cadence {avg:.1f}s differs from expected {expected_interval}s ± {tolerance}s "
                      f"(intervals={[f'{i:.1f}' for i in intervals]})")
        return
    log(f"[B] PASS: avg cadence {avg:.1f}s vs expected {expected_interval}s ± {tolerance}s")


def assert_d_disconnect(r: RunResult, errors: list[str]) -> None:
    if r.http_status != 200:
        errors.append(f"[D] expected HTTP 200, got {r.http_status}: {r.error}")
        return
    if r.thinks:
        log(f"[D] PASS: clean client drop after {len(r.thinks)} think chunk(s) "
            "(server-side stop verified by Go E2E, not by this scenario)")
        return
    # 0 thinks on a healthy 200 stream = the upstream recovered between the
    # probe and this scenario (sticky routing hands the request to a now-
    # healthy credential). The disconnect path simply was not exercisable
    # right now — record it as a skip note, not a failure.
    if r.first_data_at is not None:
        r.notes.append("skipped: upstream healthy on this attempt — retry path not observed, "
                       "disconnect-stop evidence lives in the Go E2E suite")
        log("[D] SKIP: upstream healthy (stream delivered content, no retry) — cannot exercise drop-during-retry")
        return
    errors.append("[D] connection produced no bytes at all — gateway or model misconfigured")


# ---------------------------------------------------------------------------
# reporting
# ---------------------------------------------------------------------------


def render_report(gateway: str, results: list[RunResult], errors: list[str], model: str) -> str:
    md: list[str] = ["# No-Nodes Retry Mock-Client Report", ""]
    md.append(f"- gateway: `{gateway}`")
    md.append(f"- model: `{model}`")
    md.append(f"- generated: {datetime.now(timezone.utc).isoformat()}")
    md.append("")
    for r in results:
        md.append(f"## {r.scenario}")
        md.append("")
        md.append(f"- started_at: {r.started_at}")
        md.append(f"- duration_sec: {r.duration_sec:.2f}")
        md.append(f"- http_status: {r.http_status}")
        if r.first_data_at is not None:
            md.append(f"- first_data_after_sec: {r.first_data_at - r.started_mono:.2f}")
        md.append(f"- thinks_observed: {len(r.thinks)}")
        for i, t in enumerate(r.thinks[:8], start=1):
            md.append(f"  - #{i}: attempt={t.attempt} reason={t.reason} wait_seconds={t.wait_seconds} msg={t.message[:60]!r}")
        if r.notes:
            md.append(f"- notes: {r.notes}")
        if r.error:
            md.append(f"- error: {r.error}")
        md.append("")
    md.append("## Failures" if errors else "## All assertions passed")
    md.extend(f"- {e}" for e in errors)
    return "\n".join(md) + "\n"


def main() -> int:
    p = argparse.ArgumentParser(description="No-nodes retry mock-client (see module docstring for scope)")
    p.add_argument("--gateway", default=DEFAULT_GATEWAY)
    p.add_argument("--api-key", default=DEFAULT_API_KEY)
    p.add_argument("--model", default="glm-5.2")
    p.add_argument("--interval", type=float, default=30.0,
                   help="expected think cadence in seconds (survival default 30; legacy retry is exponential — use ~6 ±8)")
    p.add_argument("--tolerance", type=float, default=10.0)
    p.add_argument("--max-thinks", type=int, default=3,
                   help="stop scenario A after this many thinks (0 = until stream end)")
    p.add_argument("--wait-for-thinks", type=int, default=2,
                   help="thinks to read before dropping the socket in scenario D")
    p.add_argument("--report-only", action="store_true",
                   help="collect results but never fail (exit 0)")
    p.add_argument("--output-prefix", default="no-nodes")
    args = p.parse_args()

    errors: list[str] = []
    results: list[RunResult] = []

    log(f"gateway={args.gateway} model={args.model} interval={args.interval}s tolerance={args.tolerance}s")
    try:
        ready = PoolManager().urlopen("GET", f"{args.gateway}/readyz", timeout=5.0).data
        if b'"ready"' not in ready:
            log(f"WARNING: gateway not ready: {ready[:120]!r}")
    except Exception as e:
        log(f"WARNING: readiness probe failed: {e}")

    def new_result(scenario: str) -> RunResult:
        return RunResult(
            scenario=scenario, model=args.model,
            started_at=datetime.now(timezone.utc).isoformat(),
            ended_at="", duration_sec=0.0, http_status=None,
            connection_alive_at_end=False,
        )

    try:
        ra = new_result("A_connection_stays_open")
        run_thinks(gateway=args.gateway, api_key=args.api_key, model=args.model,
                   interval=args.interval, max_thinks=args.max_thinks, report=ra)
        results.append(ra)
        if not args.report_only:
            assert_a_connection_stays_open(ra, errors)
            assert_b_think_cadence(ra, args.interval, args.tolerance, errors)
    except Exception as e:
        log(f"scenario A crashed: {e}")
        errors.append(f"[A] crashed: {e}")

    time.sleep(3.0)  # let gateway rate-limit windows reset between scenarios

    try:
        rd = new_result("D_client_disconnect")
        run_disconnect(gateway=args.gateway, api_key=args.api_key, model=args.model,
                       interval=args.interval, wait_for_thinks=args.wait_for_thinks, report=rd)
        results.append(rd)
        if not args.report_only:
            assert_d_disconnect(rd, errors)
    except Exception as e:
        log(f"scenario D crashed: {e}")
        errors.append(f"[D] crashed: {e}")

    REPORT_DIR.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    json_path = REPORT_DIR / f"{args.output_prefix}-{ts}.json"
    md_path = REPORT_DIR / f"{args.output_prefix}-{ts}.md"

    payload = {
        "gateway": args.gateway,
        "model": args.model,
        "interval_sec": args.interval,
        "tolerance_sec": args.tolerance,
        "results": [
            {
                "scenario": r.scenario,
                "started_at": r.started_at,
                "duration_sec": r.duration_sec,
                "http_status": r.http_status,
                "first_data_after_sec": (r.first_data_at - r.started_mono) if r.first_data_at else None,
                "thinks_observed": len(r.thinks),
                "thinks": [
                    {"attempt": t.attempt, "reason": t.reason, "wait_seconds": t.wait_seconds, "message": t.message}
                    for t in r.thinks
                ],
                "notes": r.notes,
                "error": r.error,
            }
            for r in results
        ],
        "assertion_errors": errors,
    }
    json_path.write_text(json.dumps(payload, indent=2, ensure_ascii=False, default=str))
    md_path.write_text(render_report(args.gateway, results, errors, args.model))
    log(f"wrote {json_path}")
    log(f"wrote {md_path}")

    if errors:
        log(f"FAILED ({len(errors)} assertion error(s))")
        return 1
    log("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())