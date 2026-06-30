# 184 / 71 → Local DB Sync

## Purpose

This runbook describes the reusable process for syncing `llm_gateway` from the 184 k3s production environment (or 71 host-docker replica) into the local `r112_postgres` database.

The scripts are:

- `scripts/sync-db-from-184.sh` — single-shot sync from **184** in three modes (SQL-level via `pg_dump`/`psql`)
- `scripts/sync-db-from-184-pgbase.sh` — single-shot sync from **184** via physical `pg_basebackup` (byte-level streaming replication, faster)
- `scripts/sync-db-from-71.sh` — single-shot sync from **71** in three modes (SQL-level via `pg_dump`/`psql`)
- `scripts/deploy-verify-from-184.sh` — one-click: sync + restart + smoke + report

## Server topology

| Server | IP | SSH port | SSH key | PG location | k8s? |
|---|---|---|---|---|---|
| **184** (`test-apps-apps`) | `__INTERNAL_PUBLIC_IP__` | 25022 | `~/.ssh/id_ed25519` | k8s deployment `llm-gateway-pg` in `pms-test` namespace | Yes |
| **71** (`test-apps-infra`) | `__HOST_71_IP__` | 25022 | `~/.ssh/71_id_rsa` | host docker container `llm-gateway-pg-71-replica` | No |

The 71 PG container is the same `citusdata/citus:11.3.0` image as local, runs as the `llm_gateway` superuser, and uses the password `__REDACTED_DB_PASSWORD__` (set in the container's `POSTGRES_USER` / `POSTGRES_PASSWORD`).

## Choosing a sync strategy

| Approach | Tool | Speed | Use when |
|---|---|---|---|
| `pg_basebackup` (byte-level) | `sync-db-from-184-pgbase.sh` | ~3-5 min for full 4 GB | Want a clean clone fast; willing to ALTER `replicator` role on 184 |
| `pg_dump` (SQL-level) | `sync-db-from-{184,71}.sh full` | ~10-20 min for full 4 GB | No replication access; only have `llm_gateway` (superuser) credentials |
| `pg_dump --data-only` | `sync-db-from-{184,71}.sh data-only` | ~3-5 min | Schema already aligned; only need to refresh data |

For one-shot full clones from 184, prefer `pg_basebackup` (this script). For data refresh or schema-only, use `pg_dump` modes. For 71, only `pg_dump` modes are available (no replication privilege on the host-docker container).

## Modes

### 1. `data-only`

Use when:
- local schema is already aligned
- you only need the latest production data quickly
- you want the fastest refresh path

Method:
- back up local DB
- `TRUNCATE ... RESTART IDENTITY CASCADE` all local `public` tables (skipping hot tables)
- stream `pg_dump --data-only --disable-triggers` from 184 into local (excluding hot tables)

Hot tables are skipped to avoid:
- log/data-dirty failures from malformed rows in production time-series tables
- unbounded write amplification of large append-only tables

Hot tables excluded from `data-only`:
- `request_logs` (and partitions: `request_logs_2026_07`, `request_logs_2026_08`, `request_logs_archive*`)
- `request_wal` (and partitions: `request_wal_2026_06`, `request_wal_2026_07`)
- `request_wal_bodies`
- `usage_ledger` (and partitions), `usage_minute`
- `armor_judgments`
- `routing_audit_log`, `routing_decision_log`, `route_decisions`
- `candidate_failure_logs`, `credential_probe_model_log`
- `credential_model_*` stats tables
- `model_probe_runs`, `model_probe_state`, `passive_probe_state`
- `credential_health_checks`
- `model_offer_events`, `price_change_events`, `provider_events`
- `tool_call_events`, `tool_usage_stats` (and partitions)
- `session_audit_records`, `session_memora_extraction_log`, `session_titles`
- `token_audit_events`, `response_format_anomalies`
- `model_reconcile_log`, `model_discovery_runs`, `pricing_refresh_log`
- `auto_tune_audit`, `schema_migration_audit`
- `background_tasks`, `background_tasks_duplicates`, `security_audit_log`
- `key_rpm_daily`, `api_key_model_cost`, `api_key_auto_profile`
- `credential_quota_usage`, `credential_model_call_history`

### 2. `schema-only`

Use when:
- local structure has drifted from 184
- you want to align tables, views, indexes, triggers, policies, and functions
- local data can be discarded

Method:
- back up local DB
- drop and recreate local `public` schema
- stream `pg_dump --schema-only --clean --if-exists` from 184 into local

### 3. `full`

Use when:
- you need the most accurate point-in-time clone
- both schema and data may differ
- you want the lowest ambiguity path

Method:
- back up local DB
- drop and recreate local database `llm_gateway`
- stream full `pg_dump --clean --if-exists` from 184 into local

## Commands

Sync only:

```bash
./scripts/sync-db-from-184.sh data-only
./scripts/sync-db-from-184.sh schema-only
./scripts/sync-db-from-184.sh full
```

One-click deploy + verify:

```bash
./scripts/deploy-verify-from-184.sh full
./scripts/deploy-verify-from-184.sh schema-only
./scripts/deploy-verify-from-184.sh data-only
./scripts/deploy-verify-from-184.sh --verify-only   # only smoke
./scripts/deploy-verify-from-184.sh --sync-only     # only sync
./scripts/deploy-verify-from-184.sh full --skip-smoke
```

## Verification Standard

The sync script always verifies after every run:

- `public` table count: local must equal 184
- key static tables: local row counts must equal 184
  - `approval_queue`
  - `tool_registry`
  - `tenant_model_policies`
- hot table drift check:
  - `request_logs` (allowed to drift; logged as warning)

The orchestration script additionally runs an R1.12-aware smoke test:

| Check | What it asserts |
|---|---|
| `healthz` | `/healthz` returns 200 |
| `chat_basic (tenant_id echoed)` | `/v1/chat` echoes the `X-Tenant-ID` header back as `tenant_id` |
| `chat_basic (request_id present)` | `/v1/chat` response includes `"request_id":"req-...` |
| `chat_basic (status ok)` | `/v1/chat` response includes `"status":"ok"` |
| `armor (jailbreak handled for tenant t-b)` | `/v1/chat` for a jailbreak prompt still echoes `tenant_id` (mock judge returns safe locally; armor pipeline doesn't error) |

Earlier versions of these checks asserted on `hook_durations_ms.observability.metrics` and `hook_durations_ms.security.check` inline in the `/v1/chat` response. The 2026-06-29 production build dropped the `hook_durations_ms` block from the chat response, so the checks were updated to assert on the canonical response shape (`request_id` / `status` / `tenant_id`) instead.

## Current Defaults

- Remote SSH host: `root@__INTERNAL_PUBLIC_IP__`
- Remote SSH port: `25022` (changed from default 22 in 2026-06)
- Remote k8s namespace: `pms-test`
- Remote PG deployment: `deployment/llm-gateway-pg`
- Remote DB user: `llm_gateway` (superuser; required to bypass RLS on tables like `approval_queue`)
- Local container: `r112_postgres`
- Local DB user: `kxuser`
- Local DB password: `kxpass`
- Local compose service: `gateway-v2` (container `r112_gateway_v2`)
- Local gateway URL: `http://localhost:8782`

These can be overridden with environment variables if the topology changes.

## Compatibility Notes

### `pg_dump` 15.18 → local `psql` 15.3

The remote PostgreSQL image runs `pg_dump` 15.18 (Debian), which emits `\restrict` and `\unrestrict` psql meta-commands to lock down the dump session. The local container runs `psql` 15.3 which doesn't recognize these and aborts with `invalid command \restrict`.

The script handles this automatically via `filter_dump_for_legacy_psql()` (rg-strips any line starting with `\restrict ` or `\unrestrict ` before piping into psql). These directives are session-scoped locks with no schema effect, so stripping them is safe.

If you upgrade the local `citusdata/citus` image to one with `psql` 16+, this filter becomes a no-op.

### SSH port change

In 2026-06 the 184 server moved SSH off port 22 to port 25022. The script's `REMOTE_SSH_PORT` default is 25022; override it if the topology changes again.

### 184 RLS-protected tables

`approval_queue` (and a few others) has FORCE ROW LEVEL SECURITY. Exporting with the `kxuser` role (which is not a superuser) fails on `COPY public.approval_queue ... TO stdout`. The script always uses `REMOTE_DB_USER=llm_gateway` (a superuser) for both the verification queries and the dump, so RLS is bypassed. Do not change this without also disabling RLS or running the export as a privileged user.

## Rollback

Every sync run saves a local backup dump under:

```text
/var/folders/q9/_5p60_p90ts99ybv605s8h9r0000gn/T/opencode/llmgw-db-sync-<timestamp>/
```

You can restore manually with `pg_restore` if needed.

## Process Summary

This is the operational skill behind the scripts:

1. Confirm local container and remote SSH/k8s access are available.
2. Choose the sync mode by the kind of drift you need to fix.
3. Back up local before any destructive action.
4. Use the fastest valid `pg_dump` strategy for that mode.
5. Restart the gateway so it loads the freshly synced data.
6. Wait for `/healthz` and run smoke tests.
7. Verify schema parity and key data counts immediately after sync.
8. Re-run `data-only` after a `full` sync when you want to close the gap on hot tables.

## Why Some R1.12 Smoke Defaults Don't Apply

The legacy `local-r112-smoke.sh` has two assertions that don't match what R1.12 actually exposes:

- `/metrics` returning `compression_triggered_total` — `cmd/gateway-v2/` does not register a `/metrics` route, so any request to it returns 404. Metrics are instead reported inline on `/v1/chat` responses as `hook_durations_ms.observability.metrics`.
- `dangerous_blocked` returning 403 — local armor uses a `judge_model=mock` that always returns `decision=safe`, so jailbreak prompts naturally pass through.

The orchestration script's `metrics_inline` and `armor_invoked` checks assert the real behavior of this build without modifying the shared smoke script.
