# 184 -> Local DB Sync

## Purpose

This runbook describes the reusable process for syncing `llm_gateway` from the 184 k3s production environment into the local `r112_postgres` database.

The scripts are:

- `scripts/sync-db-from-184.sh` — single-shot sync in three modes
- `scripts/deploy-verify-from-184.sh` — one-click: sync + restart + smoke + report

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
| `chat_basic` | `/v1/chat` echoes `tenant_id` and `request_id` |
| `metrics_inline` | `/v1/chat` response exposes `observability.metrics` hook duration (R1.12 doesn't have a `/metrics` route) |
| `armor_invoked` | `/v1/chat` response exposes `security.check` hook duration (proves armor pipeline ran, even when mock judge returns safe) |

## Current Defaults

- Remote SSH host: `root@14.103.112.184`
- Remote k8s namespace: `pms-test`
- Remote PG deployment: `deployment/llm-gateway-pg`
- Remote DB user: `llm_gateway` (superuser; required to bypass RLS on tables like `approval_queue`)
- Local container: `r112_postgres`
- Local DB user: `kxuser`
- Local DB password: `<TEST_DB_PASSWORD_REDACTED>`
- Local compose service: `gateway-v2` (container `r112_gateway_v2`)
- Local gateway URL: `http://localhost:8782`

These can be overridden with environment variables if the topology changes.

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
