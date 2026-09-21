# 2026-07-16 — Telemetry & audit JSONB hardening

## Trigger

Live audit of `/var/log/llm-gateway-go/gateway.stderr.log` on **245 pre-prod** revealed
5 classes of silent-write failures:

| Pattern | Count | Severity |
|---|---|---|
| `apihub watcher: register LLM asset failed (22P02)` | **170,090** | high — sustained per-minute spam |
| `telemetry request db persist failed (22P02/42703)` | 12 | medium |
| `telemetry decision db insert failed (22P02)` | 1 | low |
| `telemetry context_attrs persist failed (42P01)` | 19 | low — RCA table created after gateway start |
| `audit_log insert failed (22P02)` | 4 | medium — login events dropped |
| `candidate_failure_logger: insert failed (22021)` | 3 | low — UTF-8 in glm-5.2 response |

Underlying table health (also measured):

- `request_logs_hot`: 2191 MB total / 12 MB heap / **2171 MB TOAST** for 5,615 rows.
  Average `request_body` 247 KB / max 1.6 MB — bodies split migration (328a) never engaged
  (split table `request_logs_bodies_hot` is 0 rows).
- `session_titles` last write: 2026-06-19. `session_summaries` / `session_audit_records`:
  empty for the entire 245 lifetime.
- `request_logs_default` (the partition-promote source) does not exist on this cluster.

## What changed

Four files, all Go-side (no SQL migrations per user scope):

### `apihub/pg_store.go`

`marshalAny` / `marshalStringMap` now go through `sanitizeForJSONB` which:

- replaces NaN / +Inf / -Inf floats with `0.0` (recursively in nested maps / slices),
- scrubs invalid UTF-8 byte sequences with U+FFFD,
- strips NUL bytes (`\x00`) and other C0 control characters except `\t \n \r`.

If the resulting JSON is still not valid, the helper returns `"{}"` instead of failing the
upsert. New helper `hasC0ControlExceptWS` keeps the fast-path for clean input.

### `admin/telemetry.go`

- `persistDecisionLog` and `persistRequestLog`'s inner `tx.Exec` is wrapped by `execWithRetry`
  on a per-statement basis with 2 retries (50ms → 100ms backoff) for transient PG SQLSTATEs.
- `classifyAndCount` bumps `failTransient` / `failPermanent` atomic counters exposed via
  `(*telemetryIngester).FailCounts()`.

### `admin/users.go`

`auditLog`:

- New `sanitizeJSONBPayload` strips invalid UTF-8 / NUL bytes from the marshalled JSON
  payload before INSERT.
- The previous behaviour "log warn, return" is preserved when PG accepts the sanitized
  payload. When PG still rejects (`22P02` / `22021`), the function falls back to
  `INSERT … after_json = '{"raw":"<escaped>"}'::text::jsonb` so we never silently drop
  a login event.

### `domains/hooks/observability/telemetry/client.go`

- `Client` gained `failTransient / failPermanent / failRetried / failFallback` atomic
  counters. `Client.FailCounts()` returns a snapshot.
- `EmitRequestLog` sync path and `worker().flush` both bump the counters and the new
  fallback counter on every failure mode.

### Tests added

- `apihub/pg_store_sanitize_test.go` — 5 cases (NaN, +Inf, -Inf, nested NaN, slice with
  NaN, NUL bytes in string map, invalid UTF-8 in string map, empty map, control-byte
  scrubbing).
- `admin/audit_log_sanitize_test.go` — 5 cases (passthrough, NUL, invalid UTF-8,
  unrecoverable → null, isJSONBValidationError classifier).

## Deployment

- Built with `bash scripts/deploy-seamless.sh deploy 245 --seq 1072`. Took 75 s total,
  43 s for atomic symlink switch + restart. Health check + DB readiness both passed.
- Resulting binary version: `v2.4.6-2f2f8df0-20260715-1072` (release dir
  `/opt/llm-gateway-go/releases/1072-2f2f8df0`).
- `llmgw_session` admin cookie name fixed in cookie-injection helper (was previously
  trying `llmgw_jwt` against the SPA, but the backend only accepts `llmgw_session`).
- `sync-admin` step ran successfully — confirmed admin login still works.

## Verification

- `go build ./...` clean.
- `go vet ./...` clean.
- `go test ./apihub/... ./admin/... ./domains/hooks/observability/... ./domains/dbdegradation/...`
  all pass.
- `golangci-lint` on the changed files produces 0 issues (the 134 repo-wide issues are
  all pre-existing and not introduced by this change).
- Browser-use real-world smoke (2026-07-16 04:00 CST):
  - `https://llmgo.kxpms.cn` → 200 (Vue SPA, login modal)
  - `POST /api/auth/token` with `admin / __REDACTED_SSH_PASSWORD__` → 200, JWT issued
  - In-app navigation: `/` (仪表盘), `/request-logs`, `/sessions`, `/users`,
    `/routing-v2`, `/admin/settings`, `/ops/overview` — all render, no console errors
    observed.
  - 8 screenshots saved to `.scratch/session-save-fail-2026-07-16/screenshots/`.

## Limitations / follow-ups

- The `apihub watcher: register LLM asset failed (22P02)` rate is **unchanged** post-deploy
  because the same 3 ref_ids (1139197-200) keep failing even though the sanitized payload
  is valid JSON and a hand-written `INSERT … ON CONFLICT … DO UPDATE` against the same
  metadata succeeds via `psql`. Root cause is likely connection-level state (RLS session
  GUC / prepared statement cache) outside this fix's scope. Recommend investigating via
  `SET log_statement = 'all'` on PG temporarily and capturing the exact failing SQL.
- `request_logs_hot` body bloat mitigation is the **next** incident (still growing ~390 KB
  / row); needs the SQL migration 328a backfill to move bodies into `request_logs_bodies_hot`.
- `session_titles` / `session_summaries` are still empty — the upstream session-event
  pipeline that feeds them is dormant (out of scope for this change).