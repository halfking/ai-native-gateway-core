# requestjourney

Content-free request observation: transport-level ingress, per-tenant journey
events, and their projections across memory, Redis, and PostgreSQL.

## Write path (Recorder)

`Recorder.Apply` updates the local `Projection` synchronously (sequence
conflicts surface immediately) and then fans each accepted event out to one
bounded worker per store — `redis`, `postgres`, and `redis_ingress`. The
stores are independent: a slow or blocked PostgreSQL only builds lag in its
own queue and can never starve the Redis hot projection.

Each store worker is a bounded FIFO with an in-worker outbox:

- A failed write is parked and replayed with backoff (idempotent applies on
  both stores make replay safe). Replay success counts as `replayed`, not
  `written`.
- A write is dropped only after retries are exhausted (`write_failed`) or the
  outbox is full (`outbox_full`). Drops mark the affected observation
  `observation_degraded` in memory, increment drop/degraded counters with the
  store label, and report once through the error handler.
- Queue-full at enqueue drops only the affected store's copy; the other store
  still receives the event.
- Observation never back-pressures execution: enqueue is non-blocking, and the
  outbox is bounded by the queue capacity. It is in-memory durability — it
  survives transient store outages, not process restarts.

Per-store health is exposed via `Recorder.StoreStats()` (pending lag, written,
replayed, write/enqueue drops, sequence gaps) and the Prometheus metrics
`request_journey_recorder_{queue_depth,written_total,replayed_total,
dropped_total,degraded_total,seq_gap_total}` with a `store` label.

Sequence gaps: each journey worker tracks the last seq written per
(tenant, request); writing seq N when the store never saw earlier sequences
counts the hole, and terminal journeys release their tracker slot.

## Read path (QueryService)

List reads stay tiered: shared Redis first, PostgreSQL second, local memory
last, with infra failures degrading the observation status.

`Detail` merges instead of falling back: it unions events by
`request_id + seq` across memory, Redis, and PostgreSQL (local copy wins a
same-seq conflict) and returns the freshest view. Source disagreement is
reported in `DetailResult.Divergence` and counted in
`request_journey_query_source_divergence_total{kind}`:

- `content_conflict` — two sources disagree about the same sequence (always
  flagged).
- `redis_divergence` — Redis lags, drops, or misses a journey memory still
  holds; suppressed inside the 15s fan-out grace and after the Redis
  `DetailTTL` (key-level expiry makes absence expected).
- `postgres_gap` — durable PostgreSQL misses sequences (or the whole journey)
  the hot tiers hold once the grace has passed; PostgreSQL never expires, so
  only lag or a drop explains a gap.

A degraded, divergent, or infra-failed read still returns the merged data —
degradation is explicit, never silently presented as a complete account.
