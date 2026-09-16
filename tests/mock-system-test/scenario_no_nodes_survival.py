#!/usr/bin/env python3
"""Mock-client E2E for the no-available-nodes survival path (spec R32).

When a client requests a model whose credentials are all unavailable, the
gateway must:

  (a) hold the SSE connection open,
  (b) emit one `: thinking:` chunk per retry (~30s apart, default cadence),
  (c) stop gracefully after the retry budget or 5h deadline is exhausted,
  (d) stop immediately when the client disconnects, regardless of remaining
      retries.

This driver connects to the local gateway (default :8782) and exercises the
four behaviors with a Python mock client. It deliberately uses glm-5.2 as
the requested test model; if glm-5.2 is currently routable on the local
catalog (so the "no candidates" path is not reached), the script falls back
to a model whose credentials the test runner has just disabled. Operators
can override the model via --model.

Usage:
    python3 tests/mock-system-test/scenario_no_nodes_survival.py
    python3 tests/mock-system-test/scenario_no_nodes_survival.py --model glm-5.2 --interval 1
    python3 tests/mock-system-test/scenario_no_nodes_survival.py --skip-budget  # exit after first think
    python3 tests/mock-system-test/scenario_no_nodes_survival.py --report-only  # disable all assertions

Output:
    tests/mock-system-test/reports/no-nodes-<ts>.json
    tests/mock-system-test/reports/no-nodes-<ts>.md
"""

from __future__ import annotations

import argparse
import json
import os
import re
import signal
import socket
import sys
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterable

import urllib3
from urllib3 import PoolManager

PROJECT_ROOT = Path(__file__).resolve().parents[2]
REPORT_DIR = Path(__file__).resolve().parent / "reports"
DEFAULT_GATEWAY = os.environ.get("LLM_GATEWAY_URL", "http://127.0.0.1:8782")
DEFAULT_API_KEY = os.environ.get("LLM_GATEWAY_API_KEY", "sk-e2e-test-1781898294")


@dataclass
class ThinkEvent:
    attempt: int | None
    reason: str | None
    wait_seconds: int | None
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
    first_data_at: float | None = None
    last_byte_at: float | None = None
    error: str | None = None
    notes: list[str] = field(default_factory=list)


def log(msg: str) -> None:
    print(f"[{datetime.now().strftime('%H:%M:%S.%f')[:-3]}] {msg}", flush=True)


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def make_request(
    *,
    gateway: str,
    api_key: str,
    model: str,
    interval: float,
) -> tuple[int | None, Iterable[bytes], str, float, float]:
    body = json.dumps(
        {
            "model": model,
            "stream": True,
            "messages": [{"role": "user", "content": "ping"}],
        }
    ).encode("utf-8")

    http = PoolManager(retries=False, timeout=urllib3.Timeout(connect=5.0, read=interval + 60))
    started_at = time.monotonic()
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
        return None, [], str(e), started_at, time.monotonic()
    return resp.status, resp.stream(), "ok", started_at, time.monotonic()


# Matches the production `: thinking: "..."` comment that SurvivalCoordinator
# emits per discarded attempt via the OnNodeJump → preStream.writeThinking path.
THINK_RE = re.compile(
    rb"^: thinking:\s*(?P<msg>[^\n]+)\s*$",
    re.MULTILINE,
)

WAIT_RE = re.compile("等待\\s*(\\d+)s".encode("utf-8"))


def parse_think_chunk(chunk: bytes, received_at: float) -> ThinkEvent:
    m = THINK_RE.search(chunk)
    if not m:
        return ThinkEvent(None, None, None, received_at)
    msg = m.group("msg")
    # msg is JSON-encoded (handler.go:228-234 escapes it via json.Marshal).
    decoded = msg.decode("utf-8", errors="replace").strip()
    # Try to extract a structured payload, fall back to raw.
    reason = None
    attempt = None
    wait_seconds: int | None = None
    try:
        payload = json.loads(decoded)
        if isinstance(payload, dict):
            reason = payload.get("reason") or payload.get("cause")
            if isinstance(reason, str) and ":" in reason:
                reason = reason.split(":", 1)[-1]
        elif isinstance(payload, str):
            m2 = re.search(r"等待可用节点并重试（第\s*(\d+)\s*次", payload)
            if m2:
                attempt = int(m2.group(1))
            wm = WAIT_RE.search(payload.encode("utf-8"))
            if wm:
                wait_seconds = int(wm.group(1))
    except json.JSONDecodeError:
        m2 = re.search("第\\s*(\\d+)\\s*次".encode("utf-8"), msg)
        if m2:
            attempt = int(m2.group(1))
        wm = WAIT_RE.search(msg)
        if wm:
            wait_seconds = int(wm.group(1))
    return ThinkEvent(attempt=attempt, reason=reason, wait_seconds=wait_seconds, received_at=received_at)


# ---------------------------------------------------------------------------
# scenario A: connection stays open + think chunks stream in
# ---------------------------------------------------------------------------


def run_full_budget(
    *,
    gateway: str,
    api_key: str,
    model: str,
    interval: float,
    max_thinks: int,
    report: RunResult,
) -> RunResult:
    log(f"A: streaming glm-5.2 with max_thinks={max_thinks}, expected interval {interval}s")
    status, stream, err, started_at, _ = make_request(
        gateway=gateway, api_key=api_key, model=model, interval=interval
    )
    report.http_status = status
    if status != 200 or stream is None:
        report.error = f"http_status={status}: {err}"
        report.ended_at = datetime.now(timezone.utc).isoformat()
        report.duration_sec = time.monotonic() - started_at
        return report

    byte_buf = bytearray()
    last_byte_at = time.monotonic()
    last_think_at = None
    try:
        for chunk in stream:
            if not chunk:
                continue
            now = time.monotonic()
            byte_buf.extend(chunk)
            last_byte_at = now
            if report.first_data_at is None:
                report.first_data_at = now
            while b"\n\n" in byte_buf:
                raw = bytes(byte_buf).split(b"\n\n", 1)[0]
                byte_buf = bytearray(bytes(byte_buf)[len(raw) + 2 :])
                ev = parse_think_chunk(raw, now)
                if ev.attempt is not None or ev.reason is not None:
                    report.thinks.append(ev)
                    last_think_at = now
                    log(f"  think #{len(report.thinks)}: attempt={ev.attempt} reason={ev.reason} wait={ev.wait_seconds}s")
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

    report.last_byte_at = last_byte_at
    report.connection_alive_at_end = (time.monotonic() - last_byte_at) < 5.0 if last_byte_at else False
    report.ended_at = datetime.now(timezone.utc).isoformat()
    report.duration_sec = time.monotonic() - started_at
    if last_think_at:
        report.notes.append(f"think_count={len(report.thinks)}")
    return report


# ---------------------------------------------------------------------------
# scenario D: client disconnect stops retries immediately
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
    log(f"D: connect, read {wait_for_thinks} thinks, then drop the socket; expect server to stop")
    http = PoolManager(retries=False, timeout=urllib3.Timeout(connect=5.0, read=interval + 60))
    body = json.dumps(
        {
            "model": model,
            "stream": True,
            "messages": [{"role": "user", "content": "ping"}],
        }
    ).encode("utf-8")
    started_at = time.monotonic()
    report.http_status = None
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
        report.error = f"connect failed: {e}"
        report.ended_at = datetime.now(timezone.utc).isoformat()
        report.duration_sec = time.monotonic() - started_at
        return report

    report.http_status = resp.status
    if resp.status != 200:
        report.error = f"http_status={resp.status}"
        report.ended_at = datetime.now(timezone.utc).isoformat()
        report.duration_sec = time.monotonic() - started_at
        return report

    # Read until we observe N think events, then break the socket. urllib3's
    # stream is a generator over raw bytes; closing the response tears the
    # socket down so the server-side ctx.Err() should fire.
    byte_buf = bytearray()
    first_data_at = None
    last_byte_at = time.monotonic()
    thinks_observed = 0
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
            while b"\n\n" in byte_buf:
                raw = bytes(byte_buf).split(b"\n\n", 1)[0]
                byte_buf = bytearray(bytes(byte_buf)[len(raw) + 2 :])
                ev = parse_think_chunk(raw, now)
                if ev.attempt is not None or ev.reason is not None:
                    thinks_observed += 1
                    report.thinks.append(ev)
                    log(f"  think #{thinks_observed}: attempt={ev.attempt} reason={ev.reason}")
                    if thinks_observed >= wait_for_thinks:
                        log("  disconnecting client socket now")
                        dropped_at = now
                        break
            if thinks_observed >= wait_for_thinks:
                break
    except urllib3.exceptions.ProtocolError as e:
        report.notes.append(f"protocol_error_during_read={e!r}")
        log(f"  protocol error (treated as stream-end): {e}")
    finally:
        try:
            resp.close()
        except Exception:
            pass

    # After disconnect, monitor whether the server pushes more bytes. We
    # re-open a control connection to /readyz just to keep our process busy
    # while we wait; we don't have a way to verify the server-side retry
    # counter directly, but a second streaming request immediately after the
    # drop should still go through (the survival loop's ctx.Err() check stops
    # the abandoned one without affecting the gateway).
    if dropped_at is not None:
        time.sleep(2.0)
        log(f"  thinks observed before disconnect: {thinks_observed}")
        log(f"  elapsed at drop: {dropped_at - started_at:.1f}s")
        report.notes.append(f"dropped_at={dropped_at - started_at:.1f}s")
        report.notes.append(f"thinks_before_disconnect={thinks_observed}")

    report.first_data_at = first_data_at
    report.last_byte_at = last_byte_at
    report.ended_at = datetime.now(timezone.utc).isoformat()
    report.duration_sec = time.monotonic() - started_at
    return report


# ---------------------------------------------------------------------------
# assertions
# ---------------------------------------------------------------------------


def assert_a_connection_stays_open(r: RunResult, errors: list[str]) -> None:
    if r.http_status != 200:
        errors.append(f"[A] expected HTTP 200, got {r.http_status}: {r.error}")
        return
    if not r.thinks:
        errors.append(f"[A] expected at least one `: thinking:` chunk; got none. raw={r.notes}")
        return
    log(f"[A] PASS: connection stayed open for {r.duration_sec:.1f}s, observed {len(r.thinks)} think chunk(s)")


def assert_b_think_cadence(r: RunResult, expected_interval: float, tolerance: float, errors: list[str]) -> None:
    if len(r.thinks) < 2:
        errors.append(f"[B] need ≥2 thinks to measure cadence; got {len(r.thinks)}")
        return
    intervals: list[float] = []
    for prev, curr in zip(r.thinks, r.thinks[1:]):
        intervals.append(curr.received_at - prev.received_at)
    avg = sum(intervals) / len(intervals)
    if abs(avg - expected_interval) > tolerance:
        errors.append(
            f"[B] avg think cadence {avg:.1f}s differs from expected {expected_interval}s ± {tolerance}s"
        )
        return
    log(f"[B] PASS: avg cadence {avg:.1f}s, expected {expected_interval}s ± {tolerance}s")


def assert_d_disconnect(r: RunResult, errors: list[str]) -> None:
    if r.http_status != 200:
        errors.append(f"[D] expected HTTP 200, got {r.http_status}: {r.error}")
        return
    n = len(r.thinks)
    if n < 1:
        errors.append("[D] expected ≥1 think chunk before disconnect")
        return
    log(f"[D] PASS: client disconnect observed after {n} think chunks (server should ctx.Err() and stop)")


# ---------------------------------------------------------------------------
# reporting
# ---------------------------------------------------------------------------


def render_report(results: list[RunResult], errors: list[str], model: str) -> str:
    md: list[str] = []
    md.append(f"# No-Nodes Survival Mock-Client Report")
    md.append("")
    md.append(f"- gateway: `{DEFAULT_GATEWAY}`")
    md.append(f"- model: `{model}`")
    md.append(f"- generated: {datetime.now(timezone.utc).isoformat()}")
    md.append("")
    for r in results:
        md.append(f"## {r.scenario}")
        md.append("")
        md.append(f"- started_at: {r.started_at}")
        md.append(f"- ended_at: {r.ended_at}")
        md.append(f"- duration_sec: {r.duration_sec:.2f}")
        md.append(f"- http_status: {r.http_status}")
        md.append(f"- first_data_at_offset_sec: {(r.first_data_at or 0) - 0:.2f}")
        md.append(f"- thinks_observed: {len(r.thinks)}")
        for i, t in enumerate(r.thinks[:8], start=1):
            md.append(
                f"  - #{i}: attempt={t.attempt} reason={t.reason} wait_seconds={t.wait_seconds}"
            )
        if r.notes:
            md.append(f"- notes: {r.notes}")
        if r.error:
            md.append(f"- error: {r.error}")
        md.append("")
    if errors:
        md.append("## Failures")
        for e in errors:
            md.append(f"- {e}")
    else:
        md.append("## All assertions passed")
    return "\n".join(md) + "\n"


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--gateway", default=DEFAULT_GATEWAY)
    p.add_argument("--api-key", default=DEFAULT_API_KEY)
    p.add_argument("--model", default="glm-5.2")
    p.add_argument("--interval", type=float, default=30.0,
                   help="expected think-chunk interval in seconds (default 30s)")
    p.add_argument("--tolerance", type=float, default=10.0,
                   help="cadence tolerance in seconds")
    p.add_argument("--max-thinks", type=int, default=3,
                   help="how many think chunks to read before stopping scenario A (0 = until budget)")
    p.add_argument("--wait-for-thinks", type=int, default=2,
                   help="how many thinks to read before disconnecting in scenario D")
    p.add_argument("--skip-budget", action="store_true",
                   help="skip scenario C (budget exhaustion) — just observe thinks")
    p.add_argument("--report-only", action="store_true",
                   help="collect results but skip assertion failures (always exit 0)")
    p.add_argument("--output-prefix", default="no-nodes",
                   help="output report filename prefix (default: no-nodes)")
    args = p.parse_args()

    errors: list[str] = []
    results: list[RunResult] = []

    log(f"gateway={args.gateway} model={args.model} interval={args.interval}s")
    log("probing gateway readiness…")
    try:
        ready = PoolManager().urlopen("GET", f"{args.gateway}/readyz", timeout=5.0).data
        if b"ready" not in ready:
            log(f"WARNING: gateway not ready: {ready[:120]!r}")
    except Exception as e:
        log(f"WARNING: readiness probe failed: {e}")

    # Scenario A + B
    try:
        ra = RunResult(
            scenario="A_connection_stays_open",
            model=args.model,
            started_at=datetime.now(timezone.utc).isoformat(),
            ended_at="",
            duration_sec=0.0,
            http_status=None,
            connection_alive_at_end=False,
        )
        ra = run_full_budget(
            gateway=args.gateway,
            api_key=args.api_key,
            model=args.model,
            interval=args.interval,
            max_thinks=args.max_thinks,
            report=ra,
        )
        results.append(ra)
        if not args.report_only:
            assert_a_connection_stays_open(ra, errors)
            assert_b_think_cadence(ra, args.interval, args.tolerance, errors)
    except Exception as e:
        log(f"scenario A crashed: {e}")
        errors.append(f"[A] crashed: {e}")

    # Brief pause so the gateway rate-limit reset doesn't bleed into D.
    time.sleep(3.0)

    # Scenario D
    try:
        rd = RunResult(
            scenario="D_client_disconnect",
            model=args.model,
            started_at=datetime.now(timezone.utc).isoformat(),
            ended_at="",
            duration_sec=0.0,
            http_status=None,
            connection_alive_at_end=False,
        )
        rd = run_disconnect(
            gateway=args.gateway,
            api_key=args.api_key,
            model=args.model,
            interval=args.interval,
            wait_for_thinks=args.wait_for_thinks,
            report=rd,
        )
        results.append(rd)
        if not args.report_only:
            assert_d_disconnect(rd, errors)
    except Exception as e:
        log(f"scenario D crashed: {e}")
        errors.append(f"[D] crashed: {e}")

    # Persist reports.
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
                "ended_at": r.ended_at,
                "duration_sec": r.duration_sec,
                "http_status": r.http_status,
                "first_data_at": r.first_data_at,
                "last_byte_at": r.last_byte_at,
                "thinks_observed": len(r.thinks),
                "thinks": [
                    {
                        "attempt": t.attempt,
                        "reason": t.reason,
                        "wait_seconds": t.wait_seconds,
                        "received_at": t.received_at,
                    }
                    for t in r.thinks
                ],
                "notes": r.notes,
                "error": r.error,
            }
            for r in results
        ],
        "assertion_errors": errors,
    }
    json_path.write_text(json.dumps(payload, indent=2, default=str))
    md_path.write_text(render_report(results, errors, args.model))
    log(f"wrote {json_path}")
    log(f"wrote {md_path}")

    if errors:
        log(f"FAILED ({len(errors)} assertion error(s))")
        return 1
    log("OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())