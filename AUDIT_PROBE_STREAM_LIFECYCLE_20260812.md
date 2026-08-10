# Code Audit Report — Probe Stream Lifecycle (2026-08-12)

**Scope**: `b3f4c216`, `771d86d5`, `58eae0a6` — 自检队列 SSE 流、节点状态恢复、容量加权。
**Branch**: main (post model-iq merge)
**Auditor**: automated + manual review (server-side Explore agents were unavailable during this window; static review + targeted tests were used instead)

---

## Executive Summary

- **Critical issues (P0)**: 0
- **High-priority issues (P1)**: 3 — all fixed in this audit
- **Medium-priority issues (P2)**: 0

The three P1 findings were latent regressions in the just-merged self-check
SSE pipeline. They are independent and each had observable consequences in
production-like traffic; all are now patched with regression tests.

---

## Findings

### [P1] Loopback duplication: same instance publishes and re-receives its own Redis Pub/Sub events

**File**: `admin/probe_stream_sse.go` (`ProbeSSEHub.run` / `Publish`)

**Symptom**: every probe lifecycle transition reaches the dashboard **twice** per
gateway instance. `Publish` does an immediate local fanOut to all SSE clients;
the Redis Pub/Sub message it just emitted also returns to its own subscriber
and triggers another fanOut. Two-time delivery confuses the dedup logic on
the frontend and inflates load-stream dashboards.

**Root cause**: `Publish` writes to Redis AND fan-outs locally; the local
subscriber has no way to tell its own message apart from cross-instance
peers.

**Fix**:
- Add a per-process `instanceID` (8 hex chars, `crypto/rand`) to every hub.
- Embed `instance_id` into the Pub/Sub payload via `buildProbeNotify`.
- The local subscriber skips messages whose `instance_id` matches its own.
- Legacy payloads (no `instance_id`) are still delivered — preserves
  forward compatibility during rolling restart.

**Regression test**: `admin/probe_stream_sse_loopback_test.go`
(`TestProbeSSEHub_ShouldSkipNotification`).

---

### [P1] Stale status tiles: same taskID kept in multiple status lanes

**File**: `admin/probe_stream_redis_store.go` (`Record` → `RecordWithOrigin`)

**Symptom**: a node-probe moves pending → in-flight → ok. Each transition
ZADDs the same taskID into the new status ZSET, but **never** removes it
from the previous one. `SnapshotFromDimQueues` then returns three tiles for
one logical task on `initial_data`, so the dashboard shows the same probe
three times in different lanes.

**Root cause**: status ZSET eviction was missing. The dimension lane used a
JSON-member (which contains the status at write time) so the same taskID
ended up under two distinct member strings.

**Fix**:
- ZSET member is now the bare taskID (stable across the lifecycle).
- A per-task status hash (`llmgw:probe:task_status`) tracks the previous
  lane so each `RecordWithOrigin` issues a `ZREM` against the prior queue
  before `ZADD`-ing into the current one.
- Same cross-eviction logic for the source lane (`task_source` hash).
- `SnapshotFromDimQueues` resolves status from the detail hash, so readers
  never see a stale status even if a race leaves both writes concurrent.

**Regression test**: `admin/probe_stream_redis_store_test.go`
(`TestProbeRedisStore_Record_ClearsPrevStatusLane` + the
`buildProbeNotify` payload shape).

---

### [P1] Inconsistent task identifier across the lifecycle

**Files**:
- `bg/active_probe_emitter.go` (`publishSink`)
- `bg/probe_queue.go` (`publishProbeTask`)
- `bg/node_probe.go` (`publishProbeEvent`)

**Symptom**: the SSE dashboard sees a *pending* tile and a *terminal* tile
that don't share an ID, so the lifecycle view forks into two rows. This
contradicts the user requirement that "submitted/started/终端态" appear
as a single tile flowing across the queue UI.

**Root cause**:
- `NodeProbeWorker.publishProbeEvent` used `node_probe:<credID>:<model>`
  (stable, no attempt).
- `ActiveProbeEmitter.publishSink` (terminal) used `buildProbeRequestID`
  which mixes in attempt + success + nanosecond timestamp (high cardinality).
- `ProbeQueue.publishProbeTask` used `integrity:<queue.ID>` (changes per
  attempt because the queue ID autoincrements).

**Fix**:
- Introduce `buildNodeProbeTaskID(credID, model)` and use it from BOTH the
  worker and the emitter terminal path so node-probe lifecycle collapses
  into one tile (`node_probe:<cred>:<model>`).
- `ProbeQueue.publishProbeTask` prefers `task.DedupKey` (set at enqueue,
  preserved across retries) and only falls back to `integrity:<id>` when
  the dedup key is empty. The dedup key is the natural identifier because
  `Enqueue`'s `ON CONFLICT` uses it to recognise duplicates.
- `request_id` for `request_logs` (telemetry) is unchanged — it's a
  high-cardinality column that should remain distinct from the SSE tile.

**Regression test**: `bg/active_probe_emitter_taskid_test.go`
(`TestBuildNodeProbeTaskID_StableAcrossAttempts`).

---

## Audit Items Reviewed

- **Data flow**: every transition has both source (probe worker) and
  destination (SSE clients + Redis store). The `ProbeStreamEvent` payload
  carries credential, model, attempt, status, source, scheduled flag,
  and reasons — no unused field.
- **Concurrency safety**: `ProbeSSEHub.fanOut` snapshots the client set
  under the mutex before iterating; `removeClient` is idempotent
  (guarded by map presence). `ProbeSSEHub.Publish` is safe under
  concurrent calls because it acquires the mutex briefly and writes
  to per-client channels which are themselves buffered.
- **Compatibility**: no DB schema change in this fix set; all wire
  changes are forward-compatible (legacy payloads still deliver).
- **State machine**: status transitions are explicit and documented
  (pending → in-flight → ok/fail). The audit added the cross-state
  ZREM eviction to enforce "one task, one lane at any time".

---

## Positive Findings

- Producer-side emitters fire the SSE event **before** the telemetry
  guard, so the 自检 tab stays live even when telemetry is disabled
  (audit-verified in earlier round, still holds).
- Redis store is best-effort: a Redis hiccup never blocks the probe
  worker. The new `RecordWithOrigin` keeps this guarantee.
- The `Scheduled` flag on integrity planner / daily self-check
  events lets the UI distinguish "定时自检" from "按需自检", matching
  the product requirement.

---

## Test Coverage Added

| File | Test | Pins |
|---|---|---|
| `admin/probe_stream_sse_loopback_test.go` | `TestProbeSSEHub_ShouldSkipNotification` | Same-instance skip, cross-instance forward, legacy payload, empty instanceID |
| `admin/probe_stream_redis_store_test.go` | `TestProbeRedisStore_NotifyEnvelopeHasInstanceID` | Notify payload shape + nil-safe Record |
| `admin/probe_stream_redis_store_test.go` | `TestProbeRedisStore_Record_ClearsPrevStatusLane` | Status key uniqueness invariant |
| `bg/active_probe_emitter_taskid_test.go` | `TestBuildNodeProbeTaskID_StableAcrossAttempts` | ID stability across calls, attempt doesn't influence, safe for Redis keys |

---

**Audit Completed**: 2026-08-12  
**Follow-ups**: none open