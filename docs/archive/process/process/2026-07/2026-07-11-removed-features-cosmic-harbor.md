# Migration Guide — Removed Features (a13f4b2be, 2026-07-11)

This document tracks features removed in the opencode/cosmic-harbor
merge (commits a13f4b2be and parents). **All listed items are
intentionally deleted**. If you depended on any of them, follow the
migration path below before upgrading.

---

## Summary table

| Removed | Lines removed | Replacement | Migration effort |
|---|---|---|---|
| `admin/session_turn_snapshots.go` | 134 | `domains/memory/client/sink.go` (memora L1) | Low |
| `domains/streaming/session_turn_snapshots.go` | 184 | `domains/memory` package | Low |
| `domains/streaming/strip_vendor_dispatch.go` | 38 | `domains/streaming/executors/executor_chat.go` | Low |
| `domains/analysis/workers/memora_writeback_hook.go` | 147 | `domains/memory/client/sink.go` (auto-write) | Medium |
| `bg/recovery_probe_schedule.go` | 28 | `bg.CredentialProbeV2` (900-series) | Low |
| `domains/hooks/observability/telemetry/request_log_metadata.go` | 90 | Inlined into `request_log_pipeline.go` | Low |
| `domains/transformation/ir_enabled.go` | 18 | Always-on transformation path | Low |
| SQL migration 379 (`p14_observability_hot_table_sync`) | 295 | Subsumed by 384 | Done |
| SQL migration 378 (`license_offline_status`) | 17 | Removed (offline licensing decommissioned) | Done |
| `licensing/store_pgx_status_test.go` | 97 | Status endpoint removed | Done |

---

## Detail

### 1. `admin/session_turn_snapshots` + `domains/streaming/session_turn_snapshots`

**What was deleted**: storage helpers for per-turn snapshots of LLM
sessions, originally used by the dashboard's turn-level diff view.

**Why**: superseded by the unified Memora fact store. Every turn
snapshot is now persisted as a Memora memory via
`domains/memory/client/sink.go`, which gives the same query interface
and adds tenant-scoping.

**Migration**:

```go
// BEFORE
snap, err := h.SessionTurnSnapshotStore().Get(ctx, taskID, turnIndex)

// AFTER: query Memora for the turn's facts
mc := h.MemoraClient() // if not wired: nil-skip
if mc != nil && !mc.Disabled() {
    facts, err := mc.SmartSearch(ctx, memoraUserID, turnQuery, 8)
    // facts contains the same per-turn context
}
```

**Impact**: turn-level dashboard rendering will appear empty if your
viewer relied on direct DB queries against `session_turn_snapshots`.
Update UI to use `/api/system/memora-context/{task_id}` instead.

---

### 2. `domains/streaming/strip_vendor_dispatch`

**What was deleted**: a helper that stripped vendor-prefixed model
names from outbound chat requests before dispatch.

**Why**: redundant with the normalization already applied by
`executors/executor_chat.go` during request translation. Keeping a
parallel strip layer added latency and made protocol shape
inconsistent.

**Migration**: none — `executor_chat.go` performs the same
normalization eagerly. If you observe unstripped vendor prefixes in
upstream logs, file an issue against the executor layer rather than
re-introducing the strip helper.

---

### 3. `domains/analysis/workers/memora_writeback_hook` + test

**What was deleted**: a hook that automatically wrote structured
analysis results back into Memora after the analytics workers
finished.

**Why**: replaced by the new `memoraSink` sink (`domains/memory/client/sink.go`)
which is wired from `cmd/gateway/memory_adapter.go` and provides
**async, queue-bounded, retry-on-error** writeback. The old hook was
synchronous and could block analytics workers.

**Migration**:

```go
// BEFORE (no replacement needed — old code path is deleted)
// h.RegisterHook(membra_writeback_hook.New(mc))

// AFTER: sink is wired automatically via SetMemoraSink()
// (handled by cmd/gateway/main.go:memory_adapter.go)
// Just configure the env vars:
//   LLM_GATEWAY_MEMORA_BASE_URL=http://kxmemory:8001
//   LLM_GATEWAY_MEMORA_API_KEY=<admin>
//   LLM_GATEWAY_MEMORA_AUTO_WRITE=true
```

**Impact**: if you had custom analytics workers registering this hook
directly, remove the registration. Sink-level configuration happens
via `settings/spec_modules.go` (memora module toggle).

---

### 4. `bg/recovery_probe_schedule`

**What was deleted**: a periodic-schedule helper for the legacy
credential recovery probe.

**Why**: replaced by `bg.CredentialProbeV2` (900-series, spec §4) which
uses an event-driven trigger and an adaptive interval. The legacy
schedule had a hardcoded 30-minute cadence that missed fast-recovery
scenarios.

**Migration**: no action — `SetStateManager` (formerly
`SetCircuitResetter`) now drives recovery probes internally. External
code referencing `RecoveryProbeSchedule` will fail to compile; remove
those references.

---

### 5. `request_log_metadata.go` (observability)

**What was deleted**: a standalone telemetry helper for request-log
metadata enrichment.

**Why**: the same metadata is now produced inline by
`domains/streaming/request_log_pipeline.go` (see the inlined
`requestLogMetadata` helper added in the merge). The standalone
package conflicted with the local probe hook ordering.

**Migration**: if you imported the package directly, you must refactor
to emit metadata through the request pipeline. See
`request_log_pipeline.go:42` for the canonical hook insertion point.

---

### 6. SQL migrations 378 (`license_offline_status`) + 379 (`p14_observability_hot_table_sync`)

**378 (`license_offline_status`)**:
- **Removed**: offline-license status table + supporting indexes.
- **Reason**: offline licensing support was deprecated in v2.3;
  removal completes the cutover.
- **Action required on existing DBs**: drop the table if it exists
  before upgrading to v2.4.2+:
  ```sql
  DROP TABLE IF EXISTS license_offline_status CASCADE;
  ```

**379 (`p14_observability_hot_table_sync`)**:
- **Removed**: a 295-line migration that synced hot partition
  metadata across observability tables.
- **Reason**: superseded by migration 384 (`hot_table_independence_fix`)
  which decouples the sync logic and allows the table columns to
  evolve independently.
- **Action required on existing DBs**: migration 384 handles
  idempotent re-sync. No manual cleanup needed if you have already
  applied 379.

---

### 7. `licensing/store_pgx_status_test.go` + related crypto tests

**Removed**: tests for the licensing status endpoint and crypto
helpers, paired with `licensing/admin_api.go:186` lines of code
removal.

**Why**: offline licensing decommissioned (see 378 above).

**Migration**: n/a — if you had custom monitoring dashboards
querying the licensing admin endpoints, point them at the new
`/api/license/heartbeat` endpoint (added in 2c0f93a5e).

---

### 8. Removed test files

The following `_test.go` files were removed because the code they
covered is no longer in HEAD:

- `admin/data_lifecycle_hot_partition_test.go` — hot-partition logic
  moved to `domains/dbdegradation/` (see M1 follow-up).
- `domains/hooks/compression/session_compressor_stress_test.go` —
  stress bench now lives in `tests/session_audit/stress_bench.go`
  (a 412-line standalone binary).
- `domains/streaming/request_log_pipeline_observability_test.go` —
  superseded by `request_metadata_test.go` (240 lines, inlined).
- `domains/transformation/ir_enabled_test.go` — transformation is
  always-on; no toggle to test.
- `fault/action_executor_test.go` — fault-injection executor now uses
  the same path as recovery probe (no separate executor).
- `licensing/*_test.go` — see #7.

---

## Verification checklist

After upgrading to v2.4.2-968 (build_seq >=968), verify:

- [ ] Application compiles (`go build ./...`).
- [ ] All Go tests pass (`go test ./...`).
- [ ] `LICENSE_OFFLINE_STATUS` table is no longer referenced in
      dashboards or scripts.
- [ ] If you ran custom analytics workers, they no longer import
      `memora_writeback_hook`.
- [ ] DB migration 384 is applied:
      `SELECT version_num FROM schema_migrations WHERE version_num='384';`

If any item fails, file an issue against this migration guide with
the failing symbol / table / endpoint name.
