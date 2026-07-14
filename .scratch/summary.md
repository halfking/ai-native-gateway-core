# Session Summary

## Objective
Build a standardized probe timing calculation module (exponential backoff 5m×2ⁿ, cap 2h) to replace SQL-based `model_probe_backoff_v2`.

## Important Details
- PG on 252 (115.29.212.252:25022 → pg-252-pg17 docker, `llm_gateway` database), web gateway `llm.kxpms.cn` on 154 (47.97.111.154:25022)
- Backend binary on 154 deploys via `scp + systemctl restart llm-gateway-go`
- Backoff chosen: base=5m, multiplier=2×, cap=2h, jitter=30s
- `model_probe_backoff_v2` SQL function kept for backward compat (referenced in baseline schema + migration files)

## Completed
- **`bg/probe_backoff.go`** — New `ProbeBackoffConfig` struct with `LoadProbeBackoffConfig()` (hot-reload via settings) and `NextDelay(failures int) time.Duration`
  - Zero/negative failures → MaxDelay (2h watchdog)
  - `failures=1` → BaseDelay (5m), `failures=2` → 10m, `failures=3` → 20m, etc.
  - Capped at MaxDelay; jitter (0–30s) added on top, also capped
- **`bg/probe_backoff_test.go`** — 8 tests covering: zero/negative, base, exponential ramp, cap, jitter bounds, custom multiplier, default values
- **`settings/spec_probe.go`** — 4 new hot-reloadable settings: `probe.backoff_base_seconds` (300), `probe.backoff_max_seconds` (7200), `probe.backoff_multiplier` (2.0), `probe.backoff_jitter_seconds` (30)
- **`bg/model_probe.go:applyResult()`** — Replaced SQL-based `model_probe_backoff_v2()` call with Go-based `cfg.NextDelay(newFail)`. Healthy=NextDelay(0)=2h, broken=7d, recovering=NextDelay(newFail)
- All 8 tests pass; `go build ./...` succeeds

## Active
- (none — module fully implemented)

## Blocked
- (none)
