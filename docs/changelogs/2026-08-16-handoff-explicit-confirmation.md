# Handoff Explicit Client Confirmation Protocol

## Problem

The 202 proposal flow for explicit handoffs had no client-side confirmation
step. A handoff could be armed without the receiving client ever acknowledging
it, leaving the `handoff_log` write, `handoff_count` increment and cooldown
arming unsynchronized and replayable.

## Changes

- **202 proposal now returns confirmation material**: `handoff_id`,
  `confirmation_token` (SHA-256 hashed server-side, never echoed back in
  cleartext) and `confirmation_expires_at` (5-minute TTL).
- **New `POST /v1/handoffs/confirm` endpoint** (idempotency-keyed):
  - Atomically writes the `handoff_log`, increments `handoff_count` and arms
    the cooldown.
  - Rejects mismatched tenant / API key / token / expiry / target, or
    conflicting replays **without** accounting — no double-count, no partial
    state.
- **New table `handoff_pending_confirmations`** (migration 361): holds the
  pending handoff row with hashed token, tenant, target and expiry.
- **Background trimmer** (`bg/handoff_pending_trimmer.go`): expires stale
  pending rows past their TTL so the table cannot grow unbounded.
- **Streaming handler integration** (`domains/streaming/handoff_confirmation.go`
  + `handler.go`): the 202 proposal path issues the pending row and returns the
  confirmation token; confirmation consumes it.

## Verification

- `go build ./...`
- `go vet ./domains/hooks/handoff/... ./domains/streaming/... ./bg/...`
- `go test ./domains/hooks/handoff/... ./domains/streaming/... ./bg/...`

## Rollback

Revert `a28227176`. Drops table `handoff_pending_confirmations` via the paired
down migration and removes the pending trimmer wiring in `cmd/gateway/main.go`.
