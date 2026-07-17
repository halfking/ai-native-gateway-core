# 2026-07-18 — Live-stream idle tile flicker fix

## Symptom

On the admin Live Stream dashboard, the idle tile for a silent lane
(vendor / provider / model with no recent requests) appeared briefly after
the 5-min idle tick, then disappeared on the next request delta or
`snapshot_refresh`.

## Root cause

Two stacked bugs:

1. **Frontend dropped the idle_marker delta payload.** After the first
   round of backend-driven idle markers (commit `113de87dc`-ish), the
   `idle_marker` SSE envelope carried a complete `delta` payload with the
   idle tiles. But the frontend handler still called the (now no-op)
   `handleLaneIdleCheck(env.lane_ids, env.ts)` and returned, throwing
   away `env.delta`. The frontend was reconstructing idle tiles locally
   before — that path was disabled by `// no-op` — so idle tiles were
   never applied to the snapshot. What users saw as "idle briefly and
   then vanishes" was the screenshot artifact of the no-op's residual
   locals.

2. **`ScanAndRecordIdleMarkers` context deadline exceeded.** The shared
   Redis at 172.16.2.210:6389 holds 396K+ keys. SCAN with default `COUNT`
   (10) had to iterate 40K times to find the ~50 activity keys, which
   took longer than the 1-second timeout the SSE hub passed in. Result:
   every 10s the hub logged `WARN live stream idle marker scan failed:
   context deadline exceeded` and no idle markers were ever written to
   Redis. So the front-end had nothing to render anyway.

## Fix

Three sub-changes, all in `admin/live_stream_sse.go`,
`admin/live_stream_redis_store.go`, and
`web/src/composables/liveStreamStore.ts`:

1. **Frontend `handleEnvelope()` `idle_marker` branch** — call
   `mergeDelta(env.delta)` instead of `handleLaneIdleCheck`. `handleLaneIdleCheck`
   is kept as a no-op for envelope-type compatibility.
2. **Backend `ScanAndRecordIdleMarkers` context timeout** — `1s` → `60s`.
   The actual scan takes <1s with the COUNT fix below; 60s is just
   headroom against network blips.
3. **Backend SCAN `COUNT`** — `0` (Redis default 10) → `5000`. Reduces
   the round-trip count from ~40K to ~80, fitting comfortably in the
   new 60s budget.

## Verification

Deployed to 245 (pre-prod, version `1146-7ba14196` → `1147-ea3cd97c`).

70-second SSE capture during normal traffic:

```
idle_marker envelopes: 7 (last ts=2026-07-17T18:02:23.317045405Z)
request envelopes:    32
Idle lanes (last idle_marker envelope):
  provider:MiniMax                    1 idle tile
  provider:火山方舟 TokenPlan           1 idle tile
  provider:火山方舟 普通版              1 idle tile
  model:minimaxai/minimax-m3          1 idle tile
```

Idle lanes remained idle across 32 unrelated request events — the
`Record()` ZRem cleanup correctly targets only the lane that just got
activity, not all idle lanes.

## Followups

- Move `ScanAndRecordIdleMarkers` to a worker goroutine if scan time
  ever creeps above 1s on production-sized DBs.
- Consider evicting `liveStreamLaneRetention` of 2h → 30min for
  high-churn DBs to keep `DBSIZE` manageable.