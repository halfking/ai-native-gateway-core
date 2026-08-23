# Dispatch and Self-check Audit Fixes

## What changed

- Persisted `ProbeQueueTask.NextRunAt` when inserting a durable self-check task.
- Normalized zero schedule times to the current time without changing future schedules.
- Validated self-check command and source values at the admin API boundary.
- Applied the documented default priority of `60` and bounded manual delays to 24 hours.
- Returned Redis probe-lane transition errors to callers while retaining structured warning logs for non-shutdown failures.

## Why

The queue already exposed delayed execution and atomic Redis lane transitions, but two implementation paths hid those contracts: `next_run_at` was not included in the insert statement, and the Redis Lua result was discarded. Invalid API values also reached database constraints and became 500 responses.

## Verification

- `go test ./bg ./admin`
- `gofmt` and `git diff --check`

## Remaining audit follow-ups

- Redis governor wiring into live credential forwarders still requires a separate integration change.
- A dedicated bounded total execution queue and minute-bucket aggregation are not introduced by this correction.
- Tentative restore remains a separate state-machine follow-up; this patch does not change credential availability side effects.
