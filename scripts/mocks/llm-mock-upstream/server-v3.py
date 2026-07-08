#!/usr/bin/env python3
"""
Mock LLM upstream v3 — multi-dimensional profiled mock for routing verification.

v3 adds on top of v2:
- Real concurrency limiting (reject=503/429, queue=wait with timeout)
- Token plan quota consumption (periodic/permanent exhaustion, auto-reset window)
- RPM sliding-window rate limiting
- Configurable latency distributions (fixed/uniform/normal/bimodal/slow_start)
- Full profile API (/admin/profile GET+POST) for one-shot configuration
- /admin/quota and /admin/concurrency introspection endpoints
- Startup profile loading via MOCK_PROFILE_JSON / MOCK_PROFILE_FILE env

Run: python3 server-v3.py
Env: MOCK_PORT=18080 MOCK_TOKEN=mock-A MOCK_PROFILE_JSON='{...}' python3 server-v3.py
"""

import asyncio
import json
import math
import os
import random
import time
import uuid
from collections import deque
from datetime import datetime, timezone
from pathlib import Path

from aiohttp import web

PORT = int(os.environ.get("MOCK_PORT", "18080"))
TOKEN = os.environ.get("MOCK_TOKEN", f"mock-{PORT}")
STATE_FILE = Path(os.environ.get("MOCK_STATE_FILE", f"/tmp/mock-state-v3-{PORT}.json"))


class MockState:
    """Mutable mock state with concurrency, quota, rate-limit and latency profile."""

    def __init__(self):
        # ── Legacy mode system (from v2) ──
        self.mode = "healthy"
        self.since = datetime.now(timezone.utc).isoformat()
        self.ttl_seconds = 0
        self.latency_min_ms = 200
        self.latency_max_ms = 500

        # ── Concurrency control ──
        self.concurrency_limit = 0           # 0 = unlimited
        self.concurrency_overflow = "reject"  # "reject" | "queue"
        self.concurrency_queue_max_wait_ms = 5000
        self._concurrency_current = 0
        self._concurrency_sem = None          # lazily initialized in set_profile

        # ── Token plan quota ──
        self.token_quota_total = 0            # 0 = unlimited
        self.token_quota_used = 0
        self.token_quota_window_seconds = 0   # 0 = no periodic reset
        self.token_quota_reset_at = 0.0       # epoch seconds; 0 = not scheduled
        self.token_quota_exhausted_mode = "periodic"  # "periodic" | "permanent"

        # ── RPM sliding window ──
        self.rpm_limit = 0                    # 0 = unlimited
        self._rpm_window = deque()            # (timestamp,) entries

        # ── Latency profile ──
        self.latency_distribution = "uniform"  # fixed|uniform|normal|bimodal|slow_start
        self.latency_jitter_ms = 0
        self.slow_start_threshold = 0         # first N requests are fast, then slow
        self.slow_start_fast_max_ms = 200
        self.slow_start_slow_min_ms = 2000
        self.slow_start_slow_max_ms = 5000
        self._request_seq = 0                 # counter for slow_start

        # ── Counters ──
        self.counters = {
            "requests_total": 0,
            "requests_success": 0,
            "requests_error": 0,
            "state_changes": 0,
            "concurrency_rejections": 0,
            "concurrency_queue_timeouts": 0,
            "concurrency_peak": 0,
            "quota_rejections": 0,
            "rpm_rejections": 0,
        }
        self.history = []

    # ── Serialization ──────────────────────────────────────────────

    def to_dict(self) -> dict:
        return {
            "mode": self.mode,
            "since": self.since,
            "ttl_seconds": self.ttl_seconds,
            "latency_min_ms": self.latency_min_ms,
            "latency_max_ms": self.latency_max_ms,
            "latency_distribution": self.latency_distribution,
            "latency_jitter_ms": self.latency_jitter_ms,
            "concurrency_limit": self.concurrency_limit,
            "concurrency_overflow": self.concurrency_overflow,
            "concurrency_current": self._concurrency_current,
            "token_quota_total": self.token_quota_total,
            "token_quota_used": self.token_quota_used,
            "token_quota_remaining": max(0, self.token_quota_total - self.token_quota_used),
            "token_quota_window_seconds": self.token_quota_window_seconds,
            "token_quota_reset_at": self.token_quota_reset_at,
            "token_quota_exhausted_mode": self.token_quota_exhausted_mode,
            "rpm_limit": self.rpm_limit,
            "counters": self.counters.copy(),
            "history": self.history[-100:],
        }

    def profile_dict(self) -> dict:
        """Full profile for /admin/profile — all tunable knobs."""
        d = self.to_dict()
        d.update({
            "token": TOKEN,
            "port": PORT,
            "concurrency_queue_max_wait_ms": self.concurrency_queue_max_wait_ms,
            "slow_start_threshold": self.slow_start_threshold,
            "slow_start_fast_max_ms": self.slow_start_fast_max_ms,
            "slow_start_slow_min_ms": self.slow_start_slow_min_ms,
            "slow_start_slow_max_ms": self.slow_start_slow_max_ms,
        })
        return d

    # ── Persistence ────────────────────────────────────────────────

    def save(self):
        try:
            STATE_FILE.write_text(json.dumps(self.to_dict(), indent=2))
        except Exception:
            pass  # best-effort

    def load(self):
        if STATE_FILE.exists():
            try:
                data = json.loads(STATE_FILE.read_text())
                self.mode = data.get("mode", "healthy")
                self.since = data.get("since", self.since)
                self.ttl_seconds = data.get("ttl_seconds", 0)
                self.latency_min_ms = data.get("latency_min_ms", 200)
                self.latency_max_ms = data.get("latency_max_ms", 500)
                self.concurrency_limit = data.get("concurrency_limit", 0)
                self.concurrency_overflow = data.get("concurrency_overflow", "reject")
                self.token_quota_total = data.get("token_quota_total", 0)
                self.token_quota_used = data.get("token_quota_used", 0)
                self.token_quota_window_seconds = data.get("token_quota_window_seconds", 0)
                self.token_quota_reset_at = data.get("token_quota_reset_at", 0)
                self.token_quota_exhausted_mode = data.get("token_quota_exhausted_mode", "periodic")
                self.rpm_limit = data.get("rpm_limit", 0)
                self.latency_distribution = data.get("latency_distribution", "uniform")
                self.latency_jitter_ms = data.get("latency_jitter_ms", 0)
                self.counters = data.get("counters", self.counters)
                self.history = data.get("history", [])
                print(f"[{TOKEN}] loaded state: mode={self.mode} conc_limit={self.concurrency_limit} quota={self.token_quota_used}/{self.token_quota_total}")
            except Exception as e:
                print(f"[{TOKEN}] load failed: {e}, using defaults")
        self._init_concurrency_sem()

    # ── Concurrency ────────────────────────────────────────────────

    def _init_concurrency_sem(self):
        """Create or resize the semaphore to match concurrency_limit."""
        if self.concurrency_limit > 0:
            self._concurrency_sem = asyncio.Semaphore(self.concurrency_limit)
        else:
            self._concurrency_sem = None

    # ── Profile management ─────────────────────────────────────────

    def set_profile(self, profile: dict):
        """Apply a full or partial profile update."""
        old_limit = self.concurrency_limit

        # Mode / latency
        if "mode" in profile:
            new_mode = profile["mode"]
            valid = [
                "healthy", "slow", "rate_limited", "quota_exceeded",
                "auth_error", "server_error", "timeout", "connection_refused",
                "broken_stream", "flaky", "tool_error", "data_format_error",
                "context_length_exceeded", "invalid_request", "rate_limit_burst",
            ]
            if new_mode in valid:
                old = self.mode
                self.mode = new_mode
                self.since = datetime.now(timezone.utc).isoformat()
                self.ttl_seconds = profile.get("ttl_seconds", 0)
                self.counters["state_changes"] += 1
                self.history.append({"from": old, "to": new_mode, "at": self.since, "ttl": self.ttl_seconds})
            else:
                raise ValueError(f"invalid mode: {new_mode}")

        for k in ("latency_min_ms", "latency_max_ms", "latency_jitter_ms",
                  "slow_start_threshold", "slow_start_fast_max_ms",
                  "slow_start_slow_min_ms", "slow_start_slow_max_ms",
                  "concurrency_queue_max_wait_ms", "token_quota_total",
                  "token_quota_window_seconds", "rpm_limit"):
            if k in profile and profile[k] is not None:
                setattr(self, k, int(profile[k]))

        for k in ("latency_distribution", "concurrency_overflow", "token_quota_exhausted_mode"):
            if k in profile and profile[k] is not None:
                setattr(self, k, str(profile[k]))

        # Concurrency limit change → reinit semaphore
        if "concurrency_limit" in profile:
            self.concurrency_limit = int(profile["concurrency_limit"])
            if self.concurrency_limit != old_limit:
                self._init_concurrency_sem()

        # Token quota
        if "token_quota_used" in profile:
            self.token_quota_used = int(profile["token_quota_used"])

        # Schedule quota reset
        if self.token_quota_window_seconds > 0 and self.token_quota_total > 0:
            self.token_quota_reset_at = time.time() + self.token_quota_window_seconds

        self.save()

    def change_mode(self, new_mode, ttl_seconds=0, latency_min=None, latency_max=None):
        self.set_profile({"mode": new_mode, "ttl_seconds": ttl_seconds})
        if latency_min is not None:
            self.latency_min_ms = latency_min
        if latency_max is not None:
            self.latency_max_ms = latency_max
        self.save()
        print(f"[{TOKEN}] mode: → {new_mode} (ttl={ttl_seconds}s)")

    # ── Quota ──────────────────────────────────────────────────────

    def maybe_reset_quota(self):
        """Check if quota window has elapsed; reset if so."""
        if (self.token_quota_window_seconds > 0
                and self.token_quota_reset_at > 0
                and time.time() >= self.token_quota_reset_at):
            self.token_quota_used = 0
            self.token_quota_reset_at = time.time() + self.token_quota_window_seconds
            self.counters["quota_rejections"] = 0
            print(f"[{TOKEN}] quota window reset: used=0, next_reset in {self.token_quota_window_seconds}s")

    def quota_exhausted(self) -> bool:
        return (self.token_quota_total > 0
                and self.token_quota_used >= self.token_quota_total)

    def consume_quota(self, tokens: int):
        if self.token_quota_total > 0:
            self.token_quota_used += tokens

    # ── RPM ────────────────────────────────────────────────────────

    def _rpm_check(self) -> bool:
        """Returns True if request allowed under RPM limit."""
        if self.rpm_limit <= 0:
            return True
        now = time.time()
        cutoff = now - 60.0
        # Purge old entries
        while self._rpm_window and self._rpm_window[0] < cutoff:
            self._rpm_window.popleft()
        if len(self._rpm_window) >= self.rpm_limit:
            return False
        self._rpm_window.append(now)
        return True

    # ── Latency ────────────────────────────────────────────────────

    def sample_latency_ms(self) -> float:
        """Sample a latency value based on the configured distribution."""
        lo = self.latency_min_ms
        hi = max(self.latency_min_ms, self.latency_max_ms)
        dist = self.latency_distribution

        if dist == "fixed":
            base = float(lo)
        elif dist == "uniform":
            base = float(random.randint(lo, hi))
        elif dist == "normal":
            mean = (lo + hi) / 2.0
            std = (hi - lo) / 6.0 if hi > lo else 10.0
            base = max(lo, min(hi, random.gauss(mean, std)))
        elif dist == "bimodal":
            # 70% fast, 30% slow — simulates variable provider latency
            if random.random() < 0.7:
                base = float(random.randint(lo, lo + (hi - lo) // 3))
            else:
                base = float(random.randint(lo + 2 * (hi - lo) // 3, hi))
        elif dist == "slow_start":
            self._request_seq += 1
            if self._request_seq <= self.slow_start_threshold:
                base = float(random.randint(lo, self.slow_start_fast_max_ms))
            else:
                base = float(random.randint(self.slow_start_slow_min_ms, self.slow_start_slow_max_ms))
        else:
            base = float(random.randint(lo, hi))

        # Apply jitter
        if self.latency_jitter_ms > 0:
            base += random.uniform(-self.latency_jitter_ms, self.latency_jitter_ms)

        return max(1.0, base)


STATE = MockState()
STATE.load()

# Load startup profile from env/file
_profile_json = os.environ.get("MOCK_PROFILE_JSON")
_profile_file = os.environ.get("MOCK_PROFILE_FILE")
if _profile_json:
    try:
        STATE.set_profile(json.loads(_profile_json))
        print(f"[{TOKEN}] applied startup profile from MOCK_PROFILE_JSON")
    except Exception as e:
        print(f"[{TOKEN}] failed to apply MOCK_PROFILE_JSON: {e}")
elif _profile_file:
    try:
        with open(_profile_file) as f:
            STATE.set_profile(json.load(f))
        print(f"[{TOKEN}] applied startup profile from {_profile_file}")
    except Exception as e:
        print(f"[{TOKEN}] failed to load profile file {_profile_file}: {e}")


# ── Response builders ──────────────────────────────────────────────

def make_response(model: str, content: str, request_id: str, prompt_tokens: int = 10) -> dict:
    completion_tokens = len(content.split())
    return {
        "id": f"chatcmpl-{request_id}",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "message": {"role": "assistant", "content": content},
                "finish_reason": "stop",
            }
        ],
        "usage": {
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "total_tokens": prompt_tokens + completion_tokens,
        },
        "_mock_identity": TOKEN,
    }


def make_chunk(model: str, request_id: str, content: str) -> str:
    chunk = {
        "id": f"chatcmpl-{request_id}",
        "created": int(time.time()),
        "model": model,
        "choices": [
            {
                "index": 0,
                "delta": {"role": "assistant", "content": content},
                "finish_reason": None,
            }
        ],
    }
    return f"data: {json.dumps(chunk)}\n\n"


# ── Concurrency context manager ────────────────────────────────────

class ConcurrencyGuard:
    """Acquires/releases a concurrency slot; returns None on reject, or an
    awaitable result. Supports both 'reject' and 'queue' overflow modes."""

    def __init__(self, state: MockState):
        self.state = state
        self.acquired = False

    async def acquire(self) -> str | None:
        """Returns None on success, or an error string on reject/timeout."""
        st = self.state
        if st._concurrency_sem is None:
            self.acquired = True
            return None

        # Fast path: try_acquire without blocking
        try:
            # asyncio.Semaphore doesn't have native try_acquire, use wait_for(0)
            await asyncio.wait_for(st._concurrency_sem.acquire(), timeout=0.001)
            self.acquired = True
            st._concurrency_current += 1
            if st._concurrency_current > st.counters["concurrency_peak"]:
                st.counters["concurrency_peak"] = st._concurrency_current
            return None
        except asyncio.TimeoutError:
            pass  # fall through to overflow handling

        # Overflow handling
        if st.concurrency_overflow == "reject":
            st.counters["concurrency_rejections"] += 1
            return "concurrency_full"
        else:  # queue
            try:
                wait_s = st.concurrency_queue_max_wait_ms / 1000.0
                await asyncio.wait_for(st._concurrency_sem.acquire(), timeout=wait_s)
                self.acquired = True
                st._concurrency_current += 1
                if st._concurrency_current > st.counters["concurrency_peak"]:
                    st.counters["concurrency_peak"] = st._concurrency_current
                return None
            except asyncio.TimeoutError:
                st.counters["concurrency_queue_timeouts"] += 1
                return "concurrency_queue_timeout"

    def release(self):
        if self.acquired and self.state._concurrency_sem is not None:
            self.state._concurrency_sem.release()
            self.state._concurrency_current = max(0, self.state._concurrency_current - 1)
            self.acquired = False


# ── Chat handler ───────────────────────────────────────────────────

async def handle_chat(request: web.Request) -> web.StreamResponse:
    started = time.monotonic()
    request_id = uuid.uuid4().hex[:16]
    STATE.counters["requests_total"] += 1

    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        STATE.counters["requests_error"] += 1
        return web.json_response({"error": {"message": f"invalid JSON: {e}"}}, status=400)

    model = body.get("model", "gpt-4o")
    messages = body.get("messages", [])
    is_stream = body.get("stream", False)

    # Estimate prompt tokens from message content length (rough: ~4 chars/token)
    user_last = ""
    total_content_len = 0
    for m in messages:
        c = m.get("content", "")
        total_content_len += len(str(c))
        if m.get("role") == "user":
            user_last = c
    if isinstance(user_last, list):
        user_last = json.dumps(user_last)
    prompt_tokens = max(10, total_content_len // 4)

    mode = STATE.mode

    # TTL check
    if STATE.ttl_seconds > 0:
        elapsed_sec = time.time() - datetime.fromisoformat(STATE.since).timestamp()
        if elapsed_sec >= STATE.ttl_seconds:
            STATE.change_mode("healthy", ttl_seconds=0)
            mode = "healthy"

    # ── Quota check (before concurrency, since quota-exhausted is a hard gate) ──
    STATE.maybe_reset_quota()
    if STATE.quota_exhausted():
        STATE.counters["quota_rejections"] += 1
        STATE.counters["requests_error"] += 1
        # 402 triggers KindQuota → quota_state transition
        err_body = {
            "error": {
                "message": "quota exhausted for this period",
                "type": "insufficient_quota",
                "code": "quota_exhausted",
            }
        }
        if STATE.token_quota_exhausted_mode == "periodic":
            # Include period hint for inferQuotaRecoverAt ("month"/"周")
            err_body["error"]["detail"] = "quota will reset next month"
        else:
            err_body["error"]["detail"] = "balance permanently exhausted"
            err_body["error"]["code"] = "balance_exhausted"
        return web.json_response(err_body, status=402)

    # ── RPM check ──
    if not STATE._rpm_check():
        STATE.counters["rpm_rejections"] += 1
        STATE.counters["requests_error"] += 1
        return web.json_response(
            {"error": {"message": "rate limit exceeded", "type": "rate_limit_exceeded"}},
            status=429,
            headers={"Retry-After": "60"},
        )

    # ── Concurrency guard ──
    guard = ConcurrencyGuard(STATE)
    conc_err = await guard.acquire()
    if conc_err is not None:
        STATE.counters["requests_error"] += 1
        # 503 triggers KindConcurrent → failover (not circuit open)
        return web.json_response(
            {"error": {"message": "overloaded", "type": "overloaded_error", "detail": conc_err}},
            status=503,
            headers={"Retry-After": "2"},
        )

    try:
        # ── Mode-specific behavior (from v2, now using configurable latency) ──
        if mode == "healthy":
            delay_ms = STATE.sample_latency_ms()
            await asyncio.sleep(delay_ms / 1000.0)

        elif mode == "slow":
            delay_ms = random.randint(5000, 30000)
            await asyncio.sleep(delay_ms / 1000.0)

        elif mode == "rate_limited":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Rate limit", "type": "rate_limit_exceeded"}},
                status=429, headers={"Retry-After": "30"},
            )

        elif mode == "quota_exceeded":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Insufficient quota", "type": "insufficient_quota"}},
                status=429,
            )

        elif mode == "auth_error":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Invalid API key", "type": "invalid_api_key"}},
                status=401,
            )

        elif mode == "server_error":
            if random.random() < 0.5:
                STATE.counters["requests_error"] += 1
                return web.json_response(
                    {"error": {"message": "Internal error", "type": "server_error"}},
                    status=500,
                )
            delay_ms = STATE.sample_latency_ms()
            await asyncio.sleep(delay_ms / 1000.0)

        elif mode == "timeout":
            await asyncio.sleep(86400)
            return web.Response(status=200)

        elif mode == "connection_refused":
            STATE.counters["requests_error"] += 1
            raise web.HTTPServiceUnavailable(text="Connection refused")

        elif mode == "broken_stream":
            if is_stream:
                response = web.StreamResponse(status=200, headers={"Content-Type": "text/event-stream"})
                await response.prepare(request)
                chunk = make_chunk(model, request_id, "broken")
                await response.write(chunk.encode())
                STATE.counters["requests_error"] += 1
                if request.transport is not None:
                    request.transport.close()
                return response
            else:
                STATE.counters["requests_error"] += 1
                return web.json_response({"error": {"message": "broken"}}, status=500)

        elif mode == "flaky":
            if random.random() < 0.3:
                STATE.counters["requests_error"] += 1
                return web.json_response({"error": {"message": "flaky"}}, status=500)
            delay_ms = STATE.sample_latency_ms()
            await asyncio.sleep(delay_ms / 1000.0)

        elif mode == "tool_error":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "tool_call_failed", "type": "invalid_request_error",
                            "code": "tool_call_error", "param": "tools[0].function"}},
                status=400,
            )

        elif mode == "data_format_error":
            STATE.counters["requests_error"] += 1
            if is_stream:
                response = web.StreamResponse(status=200, headers={"Content-Type": "text/event-stream", "X-Request-Id": request_id})
                await response.prepare(request)
                partial = make_chunk(model, request_id, "unfin")
                await response.write(partial.encode())
                await asyncio.sleep(0.02)
                if request.transport is not None:
                    request.transport.close()
                return response
            else:
                return web.Response(
                    text='{"incomplete": true, "unclosed": "string', content_type="application/json", status=200,
                )

        elif mode == "context_length_exceeded":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "maximum context length is 8192 tokens",
                            "type": "invalid_request_error", "code": "context_length_exceeded", "param": "messages"}},
                status=400,
            )

        elif mode == "invalid_request":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Invalid request parameters", "type": "invalid_request_error",
                            "code": "invalid_parameter", "param": "temperature"}},
                status=400,
            )

        elif mode == "rate_limit_burst":
            STATE.counters["requests_error"] += 1
            return web.json_response(
                {"error": {"message": "Rate limit exceeded", "type": "rate_limit_exceeded"}},
                status=429, headers={"Retry-After": "1"},
            )

        else:
            delay_ms = STATE.sample_latency_ms()
            await asyncio.sleep(delay_ms / 1000.0)

        # ── Success path ──
        content = f"echo: {user_last[:80]} [mock={TOKEN}]"

        if is_stream:
            response = web.StreamResponse(status=200, headers={"Content-Type": "text/event-stream", "X-Request-Id": request_id})
            await response.prepare(request)
            for word in content.split():
                chunk = make_chunk(model, request_id, word + " ")
                await response.write(chunk.encode())
                await asyncio.sleep(0.05)
            await response.write(b"data: [DONE]\n\n")
            STATE.counters["requests_success"] += 1
            STATE.consume_quota(prompt_tokens + len(content.split()))
            elapsed = (time.monotonic() - started) * 1000
            print(f"[{TOKEN}] req={request_id} STREAM OK mode={mode} {elapsed:.0f}ms", flush=True)
            return response

        STATE.counters["requests_success"] += 1
        STATE.consume_quota(prompt_tokens + len(content.split()))
        elapsed = (time.monotonic() - started) * 1000
        print(f"[{TOKEN}] req={request_id} OK mode={mode} {elapsed:.0f}ms q={STATE.token_quota_used}/{STATE.token_quota_total}", flush=True)
        return web.json_response(make_response(model, content, request_id, prompt_tokens))

    finally:
        guard.release()
        STATE.save()


# ── Admin handlers ─────────────────────────────────────────────────

async def handle_health(_request: web.Request) -> web.Response:
    return web.json_response({"status": "ok", "token": TOKEN, "mode": STATE.mode, "since": STATE.since})


async def handle_models(_request: web.Request) -> web.Response:
    # Include both seed models and loadtest-* models so gateway discovery
    # doesn't mark our custom models as stale/unavailable.
    all_models = [
        "gpt-4o", "gpt-4o-mini", "claude-3-opus", "claude-3-sonnet",
        "glm-4", "glm-4-flash", "deepseek-chat", "o1-mini",
        "o1-preview", "qwen-turbo", "qwen-plus", "mixtral-8x7b",
        "loadtest-mini-alpha", "loadtest-mini-beta", "loadtest-mini-gamma",
        "loadtest-standard-alpha", "loadtest-standard-beta", "loadtest-standard-gamma",
        "loadtest-pro-alpha", "loadtest-pro-beta", "loadtest-pro-gamma",
        "loadtest-ultra-alpha", "loadtest-ultra-beta", "loadtest-ultra-gamma",
        "loadtest-vision-alpha", "loadtest-vision-beta", "loadtest-vision-gamma",
    ]
    return web.json_response({
        "object": "list",
        "data": [
            {"id": m, "object": "model", "created": int(time.time()), "owned_by": "local-mock"}
            for m in all_models
        ],
    })


async def handle_admin_state_get(_request: web.Request) -> web.Response:
    return web.json_response(STATE.to_dict())


async def handle_admin_state_set(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        return web.json_response({"error": f"invalid JSON: {e}"}, status=400)
    try:
        STATE.set_profile(body)
        return web.json_response({"status": "ok", "new_state": STATE.to_dict()})
    except ValueError as e:
        return web.json_response({"error": str(e)}, status=400)


async def handle_admin_reset(_request: web.Request) -> web.Response:
    STATE.mode = "healthy"
    STATE.since = datetime.now(timezone.utc).isoformat()
    STATE.ttl_seconds = 0
    STATE.token_quota_used = 0
    STATE.counters = {k: 0 for k in STATE.counters}
    STATE.counters["state_changes"] = 1
    STATE._rpm_window.clear()
    STATE._request_seq = 0
    if STATE.token_quota_window_seconds > 0 and STATE.token_quota_total > 0:
        STATE.token_quota_reset_at = time.time() + STATE.token_quota_window_seconds
    STATE.save()
    return web.json_response({"status": "ok", "message": "reset complete"})


async def handle_admin_metrics(_request: web.Request) -> web.Response:
    return web.json_response({"token": TOKEN, "counters": STATE.counters, "mode": STATE.mode,
                               "concurrency_current": STATE._concurrency_current})


async def handle_admin_history(_request: web.Request) -> web.Response:
    return web.json_response({"token": TOKEN, "history": STATE.history[-100:]})


async def handle_admin_profile_get(_request: web.Request) -> web.Response:
    return web.json_response(STATE.profile_dict())


async def handle_admin_profile_set(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        return web.json_response({"error": f"invalid JSON: {e}"}, status=400)
    try:
        STATE.set_profile(body)
        return web.json_response({"status": "ok", "profile": STATE.profile_dict()})
    except ValueError as e:
        return web.json_response({"error": str(e)}, status=400)


async def handle_admin_quota_get(_request: web.Request) -> web.Response:
    return web.json_response({
        "token": TOKEN,
        "total": STATE.token_quota_total,
        "used": STATE.token_quota_used,
        "remaining": max(0, STATE.token_quota_total - STATE.token_quota_used),
        "window_seconds": STATE.token_quota_window_seconds,
        "reset_at": STATE.token_quota_reset_at,
        "exhausted": STATE.quota_exhausted(),
        "exhausted_mode": STATE.token_quota_exhausted_mode,
    })


async def handle_admin_quota_reset(_request: web.Request) -> web.Response:
    STATE.token_quota_used = 0
    if STATE.token_quota_window_seconds > 0 and STATE.token_quota_total > 0:
        STATE.token_quota_reset_at = time.time() + STATE.token_quota_window_seconds
    STATE.counters["quota_rejections"] = 0
    STATE.save()
    return web.json_response({"status": "ok", "message": "quota reset", "used": 0})


async def handle_admin_concurrency_get(_request: web.Request) -> web.Response:
    return web.json_response({
        "token": TOKEN,
        "limit": STATE.concurrency_limit,
        "current": STATE._concurrency_current,
        "peak": STATE.counters["concurrency_peak"],
        "rejections": STATE.counters["concurrency_rejections"],
        "queue_timeouts": STATE.counters["concurrency_queue_timeouts"],
        "overflow_mode": STATE.concurrency_overflow,
    })


async def handle_admin_latency(request: web.Request) -> web.Response:
    try:
        body = await request.json()
    except (json.JSONDecodeError, Exception) as e:
        return web.json_response({"error": f"invalid JSON: {e}"}, status=400)
    STATE.set_profile(body)
    return web.json_response({
        "status": "ok", "latency_min_ms": STATE.latency_min_ms,
        "latency_max_ms": STATE.latency_max_ms,
        "latency_distribution": STATE.latency_distribution,
        "latency_jitter_ms": STATE.latency_jitter_ms,
    })


def build_app() -> web.Application:
    app = web.Application()
    app.router.add_post("/v1/chat/completions", handle_chat)
    app.router.add_get("/v1/models", handle_models)
    app.router.add_get("/healthz", handle_health)
    app.router.add_get("/", handle_health)
    # v2-compatible endpoints
    app.router.add_get("/admin/state", handle_admin_state_get)
    app.router.add_post("/admin/state", handle_admin_state_set)
    app.router.add_post("/admin/reset", handle_admin_reset)
    app.router.add_get("/admin/metrics", handle_admin_metrics)
    app.router.add_get("/admin/history", handle_admin_history)
    app.router.add_post("/admin/latency", handle_admin_latency)
    # v3 new endpoints
    app.router.add_get("/admin/profile", handle_admin_profile_get)
    app.router.add_post("/admin/profile", handle_admin_profile_set)
    app.router.add_get("/admin/quota", handle_admin_quota_get)
    app.router.add_post("/admin/quota/reset", handle_admin_quota_reset)
    app.router.add_get("/admin/concurrency", handle_admin_concurrency_get)
    return app


if __name__ == "__main__":
    HOST = os.environ.get("MOCK_HOST", "0.0.0.0")
    print(f"[{TOKEN}] starting mock v3 on {HOST}:{PORT}, mode={STATE.mode} "
          f"conc_limit={STATE.concurrency_limit} quota={STATE.token_quota_total} rpm={STATE.rpm_limit}")
    web.run_app(build_app(), host=HOST, port=PORT)
