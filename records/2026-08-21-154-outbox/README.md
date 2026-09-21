# 2026-08-21 — RequestJourney durable outbox (552) staging apply evidence

## Scope
Single migration apply: `sql/migrations/startup/552_request_journey_durable_outbox.sql`.
Migration runner target: **252 / `pg-252-pg17`** (not 154 — `aliyun-gateway-154`
points its `LLM_GATEWAY_DATABASE_URL` at this container).

## Preflight
- 154 runtime: `2.4.6-ad347ac6d-20260715-1055` (`/opt/llm-gateway-go/llm-gateway-go`),
  service `llm-gateway-go.service` active (PID 1279).
- 154 → 252 routing: `env-252.sh` on 154 sets
  `SSH_HOST=<env:HOST_252_IP>`, `PG_HOST=localhost`, `PG_PORT=15432` (PG inside
  `pg-252-pg17` container on 252).
- 252 PG version: `PostgreSQL 17.10`.
- `schema_migrations` ledger before apply: highest row `549|2026-08-21 14:04:25+08`.
  `SELECT … IN ('540'..'552')` returned 10 (540–549); **550/551/552 not yet
  applied**, three candidate outbox tables (`request_journey_observation_outbox`,
  `observation_outbox`, `observation_outbox_relay`) did not exist.
- `request_state_transitions` already present; missing `retry_at` column.

## Apply
- File: `sql/migrations/startup/552_request_journey_durable_outbox.sql`
  (89 lines, idempotent, all `ADD COLUMN IF NOT EXISTS`,
  `DROP CONSTRAINT IF EXISTS`, `CREATE TABLE IF NOT EXISTS`,
  `CREATE INDEX IF NOT EXISTS`, `DROP POLICY IF EXISTS`, `ENABLE/FORCE RLS`).
- Transport: local → 252 host → `docker cp` into `pg-252-pg17:/tmp/552.sql`.
- Execute: `psql -v ON_ERROR_STOP=1 -f /tmp/552.sql` inside the container.
- Result: `BEGIN … COMMIT` clean, no errors. Notices are all idempotent
  skips (column / constraint / policy already absent).
- After apply, manually inserted `('552', 'RequestJourney durable observation
  outbox and retry_at parity')` into `schema_migrations` (sql file does not
  self-record the version; canonical pattern matches 544/545).

## Post-apply verification
- `to_regclass('public.request_journey_observation_outbox')` = present.
- 15 columns on the outbox table match the contract
  (id, tenant_id, request_id, seq, payload, payload_hash, status, attempts,
  next_retry_at, claim_owner, claim_until, claim_fencing_token, last_error,
  created_at, updated_at).
- 5 check constraints present:
  `attempts_chk`, `claim_fence_chk`, `processing_lease_chk`, `seq_chk`,
  `status_chk` (status IN pending/processing/failed).
- 3 partial indexes created:
  `idx_request_journey_observation_outbox_due` (next_retry_at, created_at) WHERE
  status IN (pending, failed),
  `idx_request_journey_observation_outbox_lease` (claim_until, created_at) WHERE
  status = processing,
  `idx_request_journey_observation_outbox_tenant` (tenant_id, created_at).
- `request_state_transitions.retry_at` added; check constraint
  `request_state_transitions_retry_at_event_chk` (retry_at IS NULL OR
  event_type = 'retry_scheduled') present;
  `idx_state_transitions_journey_retry_at` created.
- `pg_class`: `relrowsecurity = t`, `relforcerowsecurity = t` for the outbox
  table (FORCE RLS active).
- 2 policies: `request_journey_observation_outbox_tenant_isolation` and
  `request_journey_observation_outbox_super_admin_bypass`.
- Top 5 rows in `schema_migrations` after apply: V359, 999, **552**, 549, 548.

## Behavioural validation (deferred — requires new binary)
The following validation steps were *not* performed in this session because
they require the new `2.5.0-affb8dde-20260821-1647` binary that contains the
outbox writer/reader code paths:
- `LLM_GATEWAY_INSTANCE_ID` → `HOSTNAME` → timestamp owner fallback path
- Due-batch claim + Redis/PG transient failure → failed/backoff/reclaim
- Stale `claim_fencing_token` ack/release rejection
- Outbox depth / lag metrics under live RequestJourney load
- RequestJourney latency p50/p95/p99 before vs after

154 still runs `2.4.6-ad347ac6d-20260715-1055`; the outbox table is in place
but no process writes to it yet. Behavioural checks must wait for a separate
154 deploy window.

## Rollback posture
- 552 forward-only; failure does not auto-revert schema.
- Down migration `552_request_journey_durable_outbox.down.sql` drops the
  outbox table, partial indexes, policies, `request_state_transitions.retry_at`,
  and the matching check constraint. Run **only** on explicit data-loss
  approval.
- Apply was idempotent re-runnable.

## Files
- `apply_552.log` — psql output from apply
- `post_apply_schema.txt` — column / constraint / index / RLS / policy dump
- `pre_migrations_252.txt` / `post_migrations_252.txt` — schema_migrations
  snapshots (diff = one new `552` row)
