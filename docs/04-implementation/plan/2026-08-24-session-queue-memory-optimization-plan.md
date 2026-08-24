# Session, Rotation, and Queue Memory Optimization Plan

## Scope

Implement behavior-preserving backend optimizations for the Redis-backed session
read path, credential rotation reads, and queue projection snapshot measurement.
The existing changes in `domains/dispatch/failover.go`,
`domains/dispatch/journey_test.go`, `domains/streaming/`, and `settings/` are
outside this task and must not be staged.

## Goals

1. Reuse one Redis session hash read in `GetEnrichedSession`.
2. Reuse the already loaded session hash while stopping a session and ending its
   active credential rotation.
3. Remove the separate `HGet` used by `StartCredRotation` without changing the
   Redis data format or TTL behavior.
4. Add queue projection snapshot benchmarks before choosing any implementation
   optimization. Preserve detached waterfall ownership and snapshot isolation.

## Non-goals

- Do not delete `sessionstate`, `AtomicSession`, or other parallel state models.
- Do not remove waterfall or SSE snapshot copies.
- Do not change Redis key formats, rotation JSON schema, public APIs, or queue
  snapshot ordering.
- Do not modify frontend code in this refactor.

## Logical Points and Verification

### LP1: Shared session-hash parsing

- Files: `domains/session/session.go`, `domains/session/session_state.go`.
- Change: factor parsing of an already-read session hash; make `GetStats` and
  `GetEnrichedSession` reuse the hash where possible.
- Acceptance: existing session behavior tests pass; enriched reads issue one
  `HGetAll` for session and stats, with rotation history still read separately.
- Verification: focused Go tests and Redis command-count test.

### LP2: Stop and credential rotation read reuse

- Files: `domains/session/session_state.go` and focused session tests.
- Change: pass the initial session hash into the internal rotation-ending path;
  use a Lua atomic update for `StartCredRotation` so `total_turns` is read and
  the rotation/session/TTL writes happen in one Redis round trip. Retain the
  existing list JSON representation and decode/encode on end for compatibility.
- Acceptance: stopped snapshots contain the same session and stats values as
  before; rotation entries remain backward-compatible; no extra hash read is
  performed by `StopSession`.
- Verification: miniredis behavior tests, command-count assertions, gofmt,
  focused `go test`, and `go test -race` for the session package.

### LP3: Queue projection snapshot benchmark

- Files: `domains/dispatch/queue_projection_test.go` initially; production
  code only if a benchmark-backed low-risk change is justified.
- Change: benchmark `Snapshot` and `SnapshotWaterfall` with representative
  model lanes, credential lanes, degraded state, and waterfall samples.
- Acceptance: benchmark is deterministic enough for comparison and all current
  snapshot isolation/order tests remain unchanged.
- Verification: `go test -bench='BenchmarkQueueProjection' -benchmem ./domains/dispatch`
  and focused package tests. No ownership-copy change without a separate race
  and immutability proof.

### Observed benchmark baseline

- `BenchmarkQueueProjectionSnapshot`: 41.9 us/op, 111520 B/op, 214 allocs/op.
- `BenchmarkQueueProjectionSnapshotWaterfall`: 42.2 us/op, 141616 B/op, 269 allocs/op.
- Decision: retain waterfall and SSE snapshot copies; no safe immutable-ownership
  proof exists in this task.

## Production Call-Chain Audit

Before deleting any parallel implementation, trace callers of:

- `domains/sessionstate` state-machine APIs;
- `domains/session/atomic_session.go`;
- `domains/session/state_machine.go`.

This audit is informational for this task and does not authorize deletion.

## Stop Conditions

- Redis schema compatibility or snapshot semantics become unclear.
- A benchmark does not establish a measurable low-risk optimization.
- Existing user modifications would be overwritten or need staging.
