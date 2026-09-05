# Request Body Storage Optimization Assessment

老板，本评估只记录证据和候选方案，不修改生产存储路径、代码或数据库。

## 1. Scope And Baseline

- Repository baseline: `main` / `3bcda2ed1`.
- 245 currently runs the prompt-budget release; this assessment is independent.
- Existing parallel-session changes are untouched.
- In scope: `request_logs`, `request_logs_hot`, `request_logs_bodies`, `request_logs_bodies_hot`, telemetry body construction, persistence, promotion, and cleanup.
- Out of scope: applying migrations, changing retention values, changing request payload limits, deleting historical data, and deploying a storage change.

## 2. Evidence Audit

### 2.1 Write paths

1. `domains/hooks/observability/telemetry/client.go:861-2149`
   - `insertRequestLog` opens one transaction and writes `request_logs_hot`.
   - The SQL includes three body JSONB columns.
   - `upsertRequestLogBodies` then writes request and response bodies to `request_logs_bodies_hot`.
   - Therefore the main telemetry path can persist the same request/response content in both the wide hot row and the dedicated body row.
2. `admin/telemetry.go:316-380`
   - The admin telemetry path excludes full bodies from `request_logs_hot`.
   - It writes full request/response bodies only through `request_logs_bodies_hot`.
   - The two ingestion paths therefore have different storage semantics today.
3. `cmd/gateway/main.go:1958-2010,2539,3839-3861`
   - Successful telemetry persistence invokes multiple `onPersisted` consumers, including session V2, attachments, body-size tracking, and other observers.
   - These callbacks consume the same `RequestLogEntry`; body-bearing fields remain live through the callback fan-out.

### 2.2 In-memory copies and transaction pressure

- `RequestLogEntry` stores request/response bodies as `*string` and outbound body as `json.RawMessage` (`client.go:198-200,244-252`).
- `EmitRequestLog` queues the same entry pointer when capacity is available (`client.go:563-607`). On overflow, it synchronously calls `persistRequestLog`, so the caller can pay the full body serialization and database transaction cost.
- The worker batches up to 50 entries and flushes every 200ms (`client.go:689-715`); the queue retains body-bearing entry references until flush.
- `insertRequestLog` sanitizes the entry before persistence and keeps a 5-second transaction timeout (`client.go:861-883`). The body values are then bound once to `request_logs_hot` and again to `request_logs_bodies_hot` in the same request lifecycle.
- `upsertRequestLogBodies` can summarize bodies only behind `requestBodiesSummaryEnabled`; production defaults it to `false` (`body_summary.go:62-76`).
- PostgreSQL may compress JSONB into TOAST, but the application still holds the original values through queueing, binding, and callbacks. TOAST reduction alone does not remove the gateway peak.

### 2.3 Schema and TOAST pressure

- `request_logs_hot` contains three body JSONB columns (`sql/objects/tables/request_logs_hot.sql:45-50,77`) and is otherwise wide.
- `request_logs_bodies_hot` contains the same three JSONB body columns as a dedicated heap table (`sql/objects/tables/request_logs_bodies_hot.sql:5-12`).
- `request_logs_bodies` is a range-partitioned table with the same three JSONB columns (`sql/objects/tables/request_logs_bodies.sql:5-12`).
- Migration 562 converted the current-month body partition to heap because promotion/cleanup needs updates/deletes, while preserving July columnar data (`sql/migrations/startup/562_fix_request_logs_bodies_partitions_heap.sql:9-35,86-101`). Body storage is mixed.
- The body view explicitly projects five columns and unions hot plus monthly data (`sql/objects/views/request_logs_bodies_with_current_month.sql:5-18`). Schema changes must rebuild it in the same migration.

### 2.4 Promotion and cleanup

- `PartitionManager` promotes `request_logs_bodies_hot` every cycle (`bg/partition_manager.go:735-761,790-860`). Each batch runs in a transaction and is guarded by an advisory lock.
- `promote_request_logs_bodies_hot_to_partition` deletes a hot batch and inserts it into the partitioned table (`sql/objects/functions/promote_request_logs_bodies_hot_to_partition_interval_integer.sql:25-57`). Test it against every live partition storage mode.
- A separate `promote_request_logs_bodies_default_batch` path also exists and uses a temporary table, delete, then insert with a broad exception block (`sql/objects/functions/promote_request_logs_bodies_default_batch_interval_integer.sql:11-36`). It is not listed in the current Go promote specs, so its operational ownership and reachability require explicit confirmation before reuse or removal.
- `admin/data_lifecycle_blobs.go:99-111,212-251` previews against `request_logs` but execute mode updates only `request_logs_hot`, a semantic source mismatch.
- `scripts/manage-request-logs.sh:231-234` reports temperature trimming as unimplemented; `log.trim_days` is not an active body-trimming path.
- `drop_old_request_logs_bodies_partitions` drops old monthly partitions based on retention days (`sql/objects/functions/drop_old_request_logs_bodies_partitions_integer.sql:5-38`). This is destructive and must remain behind the existing migration/operation approval process.

## 3. Main Findings

### P1: Duplicate body persistence

The main telemetry path can write full bodies into both hot tables, multiplying JSON binding, heap/TOAST writes, WAL, and retained bytes. This is the clearest target.

### P1: Application memory peak is not bounded by database compression

The entry and queue model retains full body values through batching and callback fan-out. TOAST compression cannot reduce the pre-write gateway peak.

### P1: Body lifecycle semantics are split

The body table has a dedicated 7-day lifecycle, while `request_logs` has separate cleanup/preview behavior. Resolve this before changing retention or adding copies.

### P2: Promotion behavior depends on partition storage mode

Migration 562 confirms that heap is required for current-month body partitions in the active promotion/cleanup design. A generic “compress all body partitions” change is unsafe unless it proves that no update/delete/retry path can reach the target partition.

### P2: Existing summary implementation is reversible but currently disabled

`body_summary.go` provides a test-covered digest envelope with byte count, SHA-256, and a bounded 2048-byte head. Production defaults to full bodies, so this is a canary mechanism only.

## 4. Candidate Options

| Option | Expected benefit | Main risk | Recommendation |
|---|---|---|---|
| A. Remove full bodies from `request_logs_hot`; keep them in `request_logs_bodies_hot` | Removes duplicate TOAST/WAL/storage from the wide hot table; smallest schema change | Readers that query only `request_logs_hot` lose full-body access | **First implementation** |
| B. Enable digest envelopes in body hot table | Strong reduction in PG TOAST and WAL; retains bounded forensic evidence | Loses full payload unless another durable source is guaranteed; may break replay/forensics expectations | Canary only after consumer inventory |
| C. Keep full bodies but add a bounded body-size policy | Limits worst-case memory and storage per request | May silently truncate evidence; must define error/metadata contract | Complement to A, not a replacement |
| D. Async body writer separate from metadata transaction | Removes body write latency and transaction size from request-log path | Requires durable queue/outbox, retry, backpressure, and loss semantics | Later phase |
| E. Split request/response/outbound bodies into separate tables | Allows independent retention and access patterns | More joins, migrations, indexes, and compatibility work | Only if measured data shows one body dominates |

## 5. Minimal Design Proposed For Confirmation

### Phase 1: eliminate duplicate persistence

- Change only the main telemetry `request_logs_hot` INSERT/UPSERT so the three full-body columns are not written there.
- Keep `request_logs_bodies_hot` as the sole full-body storage path.
- Keep `request_preview`, `response_preview`, checksums, body byte counts, and body summaries in metadata.
- Keep admin telemetry behavior unchanged because it already follows the dedicated body table path.
- Update readers that currently expect full bodies from `request_logs_hot` to use the explicit body view/join. Do not use `SELECT *`.

### Phase 2: bound the in-memory body lifecycle

- Add a measured body persistence maximum before queueing, recording original bytes and truncation/digest status.
- Ensure the queue overflow synchronous path applies the same policy as the worker path.
- Add tests for queue-full, large request, large response, and callback fan-out behavior.

### Phase 3: optional digest canary

- Enable the existing digest envelope only for an explicit tenant or platform-scoped canary.
- Measure body byte reduction, read compatibility, replay impact, and forensic usefulness.
- Do not globally enable until all consumers of complete bodies are classified as either required or digest-compatible.

## 6. Rollback Plan

### Phase 1 rollback

- Application-only rollback: restore the previous telemetry INSERT/UPSERT body assignments and redeploy the prior release.
- No data migration is required because the dedicated body table remains populated during the canary.
- Existing rows in `request_logs_hot` remain readable; rollback restores future writes only.
- Verification after rollback: compare body presence rates in `request_logs_hot` and `request_logs_bodies_hot`, then run request-log detail and session replay smoke tests.

### Phase 2 rollback

- Disable the new body-size policy through the hot-reload setting or revert the application release, depending on the final contract.
- Do not delete or rewrite existing body rows during rollback.

### Phase 3 rollback

- Turn off the digest flag. Existing digest envelopes remain readable by the current compatibility readers; future writes return to full body persistence.
- If a consumer cannot read the envelope, use the dedicated body table backup/release rollback rather than rewriting historical rows.

## 7. Required Verification Before Implementation

1. Query live 245 and 252 schema metadata for all body tables, primary keys, indexes, partition bounds, access methods, and view columns. Redact credentials and do not modify data.
2. Trace all production readers of `request_logs.request_body`, `request_logs.response_body`, `request_logs.outbound_body`, and `request_logs_bodies_with_current_month`.
3. Measure, by time window, `pg_total_relation_size`, `pg_relation_size`, `pg_indexes_size`, and TOAST size for both hot tables and body partitions.
4. Measure body size percentiles at the gateway: request, response, outbound, and combined bytes; include queue-full synchronous writes.
5. Run a local production-like PG/columnar validation before any migration. The migration must include explicit view rebuild and schema-truth verification.
6. Add regression tests proving: metadata remains queryable, full body reads still work, promotion is atomic, and rollback restores body persistence.
7. Run `go test`, `go vet`, relevant integration tests, security review, and the 245 deployment gate only after human approval of the design.

## 8. Decision Gate

No implementation should start until the owner confirms:

- whether `request_logs_hot` is allowed to stop carrying full bodies;
- which readers require complete bodies and which can read the dedicated body view;
- whether body retention is 7 days for all tenants or needs a tenant/platform exception;
- the maximum accepted in-memory body size and truncation contract;
- whether a durable async body queue is required now or can be deferred.
