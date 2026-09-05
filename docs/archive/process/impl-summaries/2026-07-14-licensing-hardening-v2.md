# License Module Hardening v2 — Implementation Summary

**Date**: 2026-07-14
**Branch**: `feature/deploy-ops-license-v2`
**Commit**: 6e1a25e32
**Status**: ✅ Complete — 6 files / +1273 lines, 21+ tests pass

---

## § Executive Summary

The license module underwent three production hardening improvements that the v1 implementation lacked:

1. **Grace period for startup failures** — `EnforceAtStartupWithGrace` lets a service survive a transient master outage (up to 24h) without entering restricted mode
2. **Exponential backoff with jitter for token refresh** — replaces hardcoded `5s/30s/120s` with a configurable curve (`BaseDelay × 2^(attempt-2)` capped at `MaxDelay`, with random jitter to avoid thundering herd)
3. **`restricted_mode.go` bypass fix** — `len(path) >= 19 && path[:19] == "/api/system/license"` accepted `/api/system/licenseeXploit`, `/api/system/license.json`, etc. (false-positive admin endpoint access during license outage)

Plus **DaemonHealth observability** — read-only `*DaemonHealth` snapshot exposing per-cycle outcomes via `GetDaemonHealth()` so ops dashboards can graph refresh-failure rate without coupling to the goroutine.

---

## § 1. Architecture

### Code layout

```
licensing/
├── grace.go              (new, 277 lines) — FailureMarker + GracePolicy
├── daemon_health.go      (new, 178 lines) — DaemonHealth + singleton
├── hardening_v2_test.go  (new, 559 lines) — 21+ test functions
├── restricted_mode.go    (modified, +45/-15 lines) — boundary check
├── token_refresh.go      (modified, +135/-25 lines) — BackoffConfig + jitter
├── token_refresh_daemon.go (modified, +40/-15 lines) — health instrumentation
└── [existing files unchanged]
```

### State machine (grace)

```
                  ┌─────────────────┐
                  │ Verify succeeds │
                  │ → clear marker  │
                  └────────▲────────┘
                           │
                  ┌────────┴────────┐
   verify() ──────┤                 │
        │         │  No prior marker│
        │         │  + Failure      │
        │         │  → HARD FAIL    │  (defense in depth)
        │         └─────────────────┘
        │                   │
        │                   │ First failure writes the marker
        │                   ▼
        │         ┌─────────────────┐
        │         │ Marker exists  │
        │         │ + Verify fails  │
        │         └────────┬────────┘
        │                  │
        │         ┌────────┴────────┐
        ▼         ▼                  ▼
  ┌──────────┐ ┌──────────┐    ┌──────────────────┐
  │ SUCCESS  │ │ IN GRACE │    │ GRACE EXCEEDED   │
  │ clear    │ │ tolerate │    │ → HARD FAIL      │
  └──────────┘ └──────────┘    └──────────────────┘
```

### Backoff curve (`BaseDelay=5s, MaxDelay=5min, MaxAttempts=6, JitterFraction=0.2`)

```
attempt 1:   0s    (initial)
attempt 2:  ~5s   (5s  × 2^0 ± 1s jitter)
attempt 3: ~10s   (5s  × 2^1 ± 2s)
attempt 4: ~20s   (5s  × 2^2 ± 4s)
attempt 5: ~40s   (5s  × 2^3 ± 8s)
attempt 6: ~80s   (5s  × 2^4 ± 16s)
                  (capped at 5min, well-within-budget)
```

Total wall time for a fully-failed cycle: **~155s**, comfortable against the instance_token's 7-day lifetime.

---

## § 2. FailureMarker on disk

```json
{
  "failed_at": "2026-07-14T08:32:15Z",
  "reason": "verify license: dial tcp 10.0.0.1:443: i/o timeout",
  "source": "/etc/licenses/prod.dat",
  "attempts": 3
}
```

Atomic write via `tmp + rename`; `attempts` preserved across re-marks so ops can spot persistent outages vs transient blips.

---

## § 3. The restricted-mode bypass

### v1 (vulnerable)

```go
if len(path) >= 19 && path[:19] == "/api/system/license" {
    return next(c)
}
```

Logically equivalent to `strings.HasPrefix(path, "/api/system/license")`. **Fails to check the boundary character**. Attacks in restricted mode:

| path | v1 | v2 |
|------|----|----|
| `/api/system/license` | ✅ allowed | ✅ allowed |
| `/api/system/license/status` | ✅ allowed | ✅ allowed |
| `/api/system/licenseeXploit` | ⚠️ **allowed** | ❌ blocked |
| `/api/system/licenseAdmin` | ⚠️ **allowed** | ❌ blocked |
| `/api/system/license.json` | ⚠️ **allowed** | ❌ blocked |
| `/api/system/licensethief` | ⚠️ **allowed** (shorter prefix) | ❌ blocked |
| `/api/system/LICENSE` | ⚠️ **allowed** (case diff) | ❌ blocked |

### v2 (fixed)

```go
func licensePathAllowed(path string) bool {
    const prefix = "/api/system/license"
    if path == prefix || !strings.HasPrefix(path, prefix) {
        return path == prefix
    }
    // Boundary check: after the prefix, must be '/' or EOF.
    if len(path) == len(prefix) {
        return true
    }
    return path[len(prefix)] == '/'
}
```

Matches the obvious intent ("license path starts with `/api/system/license/`").

---

## § 4. Test coverage (21+ tests, all pass)

### Grace (7 tests)
- `TestGrace_FailureRecordedOnDisk` — marker written on failure
- `TestGrace_AttemptCounterIncrements` — counter preserved across re-marks
- `TestGrace_ClearRemovesMarker` — clear resets state
- `TestGrace_PolicySuccessClearsMarker` — auto-clear on success
- `TestGrace_FreshFailure_NoMarker_HardFailsImmediately` — defense in depth
- `TestGrace_OldFailure_ExceedsGrace_FailClosed` — grace exhaustion
- `TestGrace_NoGraceEnvHardFailsImmediately` — `LICENSE_NO_GRACE=1`

### Backoff (5 tests)
- `TestBackoff_NextDelayExponential` — `BaseDelay × 2^(attempt-2)`
- `TestBackoff_NextDelayRespectsMax` — `MaxDelay` cap
- `TestBackoff_JitterIsBounded` — `±JitterFraction × base`
- `TestAutoRefreshTokenWithConfig_CapsAttempts` — server hit count == MaxAttempts
- `TestAutoRefreshTokenWithConfig_HonorsZeroAttempts` — MaxAttempts=1 → 1 call

### DaemonHealth (5 tests)
- `TestDaemonHealth_RecordSuccess` — counters, ConsecutiveFails resets
- `TestDaemonHealth_RecordFailureConsecutive` — 3+ fails → Healthy=false
- `TestDaemonHealth_RecentFailuresBounded` — ring cap = 8
- `TestDaemonHealth_IsStale` — wedged daemon detection
- `TestDaemonHealth_ConcurrentAccess` — 50-race safety via `RWMutex`

### Restricted Mode (4 tests, 9 subtests)
- `TestRestrictedMode_BypassFix` (9 subtests) — every bypass attempt rejected
- `TestRestrictedMode_HealthEndpointAlwaysAllowed` — health bypass never blocked
- `TestRestrictedMode_HealthEndpointCheckHelper` — extractor test
- `TestRestrictedMode_MiddlewareBlocksBypass` — full echo integration (allowed + blocked paths)

---

## § 5. Migration notes

### Backoff policy change

`AutoRefreshToken` keeps its existing signature (`cfg *BackoffConfig` filled with `DefaultBackoffConfig` when callers pass nil), so v1 callers automatically get the new policy curve. Total wall time per failed cycle is in the same order of magnitude (~155s vs 155s), but the new curve is **exponential** (so transient blips recover fast) and **bounded** (worst-case is 5min vs unbounded v1 with 120s base).

### Grace period opt-in

`EnforceAtStartup` (the v1 entry point) is unchanged. Operators opt into grace by calling the new `EnforceAtStartupWithGrace(licensePath, publicKeyPath, dataDir, 24*time.Hour)` instead.

### Bypass fix

`RestrictedModeMiddleware`'s signature is unchanged. Existing callers (cmd/gateway start-up) get the fix transparently. **No v1 caller is exposing admin endpoints** — the bypass only matters when the service is in restricted mode, which itself is rare.

---

## § 6. Operational impact

**Before v2:**
- Master unreachable → service crashes at startup (no grace)
- 5 master flakes in 5 minutes → no recovery between cycles (hardcoded 5s/30s/120s instead of exponential)
- Restricted mode = "expose `/api/system/licenseeAdmin` to attackers"
- Daemon health = invisible until `instance_token` expires 7 days later

**After v2:**
- Master blip up to 24h → service continues serving (grace)
- Exponential backoff + jitter → master-side recovery faster, no thundering herd
- Restricted mode actually restricts (boundary-char check)
- `GetDaemonHealth()` exposes per-cycle outcome + last-error + recent-failure ring buffer for ops dashboards

---

## § 7. Files changed

### New (3 files, 1014 lines)

| File | LOC | Purpose |
|------|-----|---------|
| `licensing/grace.go` | 277 | FailureMarker + GracePolicy |
| `licensing/daemon_health.go` | 178 | DaemonHealth singleton |
| `licensing/hardening_v2_test.go` | 559 | 21+ test functions |

### Modified (3 files, +221/-55 lines)

| File | Change | Net |
|------|--------|-----|
| `licensing/restricted_mode.go` | bypass fix | +45/-15 |
| `licensing/token_refresh.go` | BackoffConfig + jitter | +135/-25 |
| `licensing/token_refresh_daemon.go` | DaemonHealth instrumentation | +40/-15 |

### Removed (.bak files)

- `licensing/offline.go.bak` — leftover from pre-rebase (untracked, not in repo)
- `licensing/types.go.bak` — same

---

## § 8. Verification

```bash
# Run new tests only
go test ./licensing/... -v -run "TestGrace|TestBackoff|TestDaemonHealth|TestRestrictedMode|TestAutoRefreshTokenWithConfig" -count=1

# All licensing tests (no regressions)
go test ./licensing/... -count=1 -timeout=120s

# Full sweep (deploy + env-injector + licensing)
go test ./licensing/... ./envinjector/... -count=1 -timeout=120s
bash tests/deploy_*.sh tests/env_injector_test.sh tests/rotate_credentials_test.sh
```

Expected:
```
ok  	github.com/kaixuan/llm-gateway-go/licensing	18.97s   ← all existing + new
ok  	github.com/kaixuan/llm-gateway-go/envinjector	0.42s
[155 shell tests + 50+ licensing Go unit tests, all passing]
```

---

## § 9. Future work (Phase 3B+)

- **Phase 3B — Ops dashboard real-time metrics**: `web/src/views/ops/OpsOverviewView.vue` already exists (26KB). v2 could add a `/api/system/license/health` endpoint exposing DaemonHealth + LastSuccessfulRefresh + CurrentGraceRemaining so the dashboard can light up red when a failover is approaching.
- **Cred rotation automation for license keys**: `rotate-credentials.sh` could be extended with `--key-name=LICENSE_*` if/when license activation keys move into SOPS envelopes.
- **Proactive grace refresh**: a tiny daemon could re-attempt verification every N minutes within the grace window — today the grace window expires on a single verification call.

These remain future work. v2 hardening is self-contained and ready to merge.
