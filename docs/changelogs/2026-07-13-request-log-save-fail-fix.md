# 2026-07-13 P0 fix: request_log writes blocked after audit-ir-multimodal deploy

## Root cause

`commit 5447bf6b1 feat(ir): support multimodal and cache token billing
(audit-ir-multimodal)` (2026-07-13 05:03) extended `RequestLogEntry`
with 5 new fields (`reasoning_tokens`, `image_tokens`, `audio_tokens`,
`video_tokens`, `provider_tokens`) and rewrote the `INSERT INTO
request_logs_hot` in `domains/hooks/observability/telemetry/client.go`
to write them.

The accompanying migration `350_multimodal_token_fields.sql` only added
the columns to the `request_logs` **parent** table. The 2026-07
data-lifecycle architecture however writes exclusively to the
independent heap table `request_logs_hot` (see migration
`341_hot_table_independence.sql`), so production INSERTs immediately
failed with:

```
ERROR: column "reasoning_tokens" of relation "request_logs_hot" does not exist
```

Because every request log INSERT runs inside a single transaction that
also writes to `usage_ledger_hot` and `api_keys`, the rollback wiped the
`usage_ledger_hot` row too. Last successful INSERT into
`request_logs_hot` was 2026-07-13 05:41; `usage_ledger_hot` was frozen
at 05:06 — right after the v990 binary deployed at 05:52.

The failure was almost invisible: `client.go` swallows the error after
a successful `fallback.WriteRequestLog(...)` to
`data/backups/backups/sessions-YYYY-MM-DD-NN.jsonl.gz`, so the
gateway.log shows **zero** `telemetry request db persist failed`
warnings for the broken period — yet ~105MB of fallback files piled
up.

A second, latent bug: even if the columns had been present, the
commit's `INSERT INTO request_logs_hot (...) VALUES (...)` had a
placeholder-numbering error starting at line 684
(`$22, $23, $24, $25, $26` duplicated the multimodal block), which
made every subsequent column mis-bind. The companion `UPDATE` SET
clause (line 988+) had the same error: `cost_display = COALESCE($17, ...)`
referenced `image_tokens`, `success = COALESCE($34, ...)` referenced
`egress_protocol`, etc.

## Impact

- `request_logs_hot` insert path was dead for ~2 hours after v990 deploy.
  Every successful /v1/chat/completions call returned 200 to the
  client but produced no audit row in PostgreSQL — only a fallback
  file.
- `usage_ledger_hot` inserts rolled back (transaction coupling with
  request_logs_hot INSERT). All billing aggregations that joined
  against the last 7 days were silently understated.
- `/api/admin/live-stream` showed `in_progress` rows whose UPDATE
  path also bound the wrong columns — even after the schema fix, an
  inserted row would never transition to `success` with the correct
  token counts.

## Fix

### 1. SQL migration `sql/migrations/startup/2026-07-13-multimodal-token-fields-hot.sql`
   (mirrored to `deploy/sql/migrations/`) — idempotent, applied to
   production on 2026-07-13 06:32 (252 / pg-252-pg17):

   - `ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
     reasoning_tokens / image_tokens / audio_tokens / video_tokens /
     provider_tokens INT DEFAULT NULL` (the actual write target)
   - Same 5 columns on `usage_ledger_hot` so billing aggregations
     stay coherent
   - Same 5 columns on `request_logs` parent + every existing
     monthly partition (`request_logs_default`,
     `request_logs_YYYY_MM`) — PG 11+ propagates parent ADD COLUMN
     to partitions automatically, but we re-verify after apply.
   - `CREATE INDEX IF NOT EXISTS
     idx_request_logs_hot_multimodal_usage` on
     `(tenant_id, ts DESC) WHERE image_tokens > 0 OR audio_tokens >
     0 OR video_tokens > 0` — supports future billing rollups by
     modality.

### 2. Go code — `domains/hooks/observability/telemetry/client.go`

   - `insertRequestLog` VALUES clause: rewritten $2..$79 in lock-step
     with the column list so `latency_ms = $27`, `success = $28`,
     `request_status = $29`, etc. match the field order set in the
     surrounding `INSERT (..., affinity_hit, prompt_tokens,
     completion_tokens, cache_read_tokens, cache_write_tokens,
     reasoning_tokens, image_tokens, audio_tokens, video_tokens,
     provider_tokens, total_tokens, cost_usd, ...)` column list.

   - `updateRequestLog` SET clause + VALUES list: same re-numbering.
     The CTE on `request_logs_hot` now correctly applies
     `cost_usd = COALESCE($21, cost_usd)` (was: `$21 -> stream_done_received`
     bind), `success = COALESCE($39, success)`, etc. Without this
     UPDATE row stayed at `success=false / total_tokens=NULL` even
     after the request actually completed.

### 3. Verification (post-deploy, on https://llm.kxpms.cn):

```sql
-- Before fix: max(ts) was 2026-07-13 05:41:14
SELECT MAX(ts), count(*) FROM request_logs_hot;
--  max              | count
--  2026-07-13 07:06:10 | 1178   ← recovered, +3 rows in 2 minutes

SELECT request_id, ts, client_model, prompt_tokens, completion_tokens,
       total_tokens, success, stream_chunk_count, request_status
  FROM request_logs_hot WHERE ts > now() - interval '5 minutes';
-- e75330e4d15bdf4f1646b73ab80a55cf | 07:06:10 | claude-sonnet-5 | 5 | 44 | 49 | t | 5 | success
-- d57f5adfffac32997481944da6202b64 | 07:04:49 | claude-sonnet-5 | 5 | 14 | 19 | t |   | success
-- 9208cd05fe21634222a9826babe5cf9a | 07:00:28 | minimax-text-01 |   |    |    | f |   | in_progress (pre-fix row, expected)
```

Both INSERT and UPDATE paths now bind the right columns. `success`
flips from `f` to `t` and `total_tokens` populates on completion.

### 4. Follow-up

- The `data/backups/backups/sessions-2026-07-13-02.jsonl.gz` (~104MB)
  and earlier files hold the rows dropped during the 2-hour outage.
  Once a separate replay task confirms no duplicate inserts, the
  `replay` path can re-ingest them via
  `dbdegradation.Recovery.ReplayFallback(ctx, record)` against
  `telemetryClient.ReplayFallback` — both already exercise the now-
  fixed INSERT path.

## Files

- `sql/migrations/startup/2026-07-13-multimodal-token-fields-hot.sql`
- `deploy/sql/migrations/2026-07-13-multimodal-token-fields-hot.sql`
- `domains/hooks/observability/telemetry/client.go` (158 lines diff)
- `docs/changelogs/2026-07-13-request-log-save-fail-fix.md` (this file)

## Rollback

If a regression appears, revert to `llm-gateway-go.v989.linux.amd64`
(pre-multimodal). The schema changes are additive (NULLable
columns) so the older binary keeps working.