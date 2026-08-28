# ADR: `JournalSnapshot()` requestjourney consumption boundary

- **Status:** Proposed
- **Date:** 2026-08-28
- **Scope:** requestjourney diagnostics and persistence

## Context

`JournalSnapshot()` is useful for reconstructing a request's state, but a snapshot
is a read model rather than an additional source of truth. The requestjourney
Redis store already owns the ordered ingress/detail event projections, while
request completion and request-log persistence have separate durability and
retention policies. Exposing a snapshot without defining its consumer risks
creating a second, unbounded journal schema or allowing diagnostic reads to
bypass tenant and operator authorization.

## Decision

Adopt a **bounded, authorized consumer** boundary before adding a new production
consumer:

1. A consumer requests a snapshot for one tenant and request ID through the
   existing requestjourney service boundary; it does not scan Redis keys or
   construct snapshots from unrelated stores.
2. The requestjourney store remains the read-model source for snapshot events.
   Existing request-log/request-detail stores remain the system of record for
   durable request and response bodies; `JournalSnapshot()` must not duplicate
   those bodies.
3. Snapshot materialization is bounded by the existing event retention and a
   maximum event count/serialized size. Truncation is explicit in the returned
   metadata rather than silently dropping events.
4. Consumers must pass the caller's tenant and authorization context. An
   unauthorized request returns the same not-found-shaped result used by the
   detail path, avoiding cross-tenant existence leaks.
5. Persistence is opt-in and asynchronous: only a compact snapshot summary and
   integrity metadata may be persisted for audit/debug use. A failed diagnostic
   persistence attempt must not change request settlement or upstream response
   behavior, but must be observable.
6. Repeated consumption is idempotent by `(tenant_id, request_id, snapshot_version)`;
   restart/retry must not append duplicate summaries.

## Alternatives considered

- **Persist every `JourneyEvent` indefinitely:** rejected because it expands the
  event schema and retention cost without a current consumer contract.
- **Reintroduce an unrestricted diagnostic query API:** rejected because it
  weakens tenant isolation and makes Redis retention the accidental authority.
- **Copy full request/response bodies into snapshots:** rejected because body
  storage already has an SSOT and separate redaction/retention rules.

## Decision-history boundary

`JournalSnapshot()` is a diagnostic decision projection, not the complete live
retry history. A history-aware action decision combines it with actual dispatch
attempt facts, the request's monotonic tried-credential/model sets, and durable
attempt/settlement state when execution crosses a process boundary. The
RequestJourney projection is intentionally not read back into the live retry
loop: delayed or replayed observations must not cause a request to forget a
prior retry or revisit an exhausted node.

## Consequences

The next implementation should add a small consumer interface and contract
tests for authorization, bounded/truncated snapshots, duplicate retries, and
persistence failure isolation. No new consumer should be wired until those
contracts are implemented and reviewed.
