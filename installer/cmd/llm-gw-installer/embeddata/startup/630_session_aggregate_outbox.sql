-- Migration 630: session_aggregate_outbox — durable retry queue for
-- Session V2 aggregate snapshot updates.
-- audit-data-closure-C (2026-08-31): the current best-effort in-process retry
-- in session_writer_v2.updateSessionAggregate loops aggregateMaxAttempts=3
-- with aggregateRetryDelay=25ms*attempt. If all three attempts fail (DB
-- unavailable, advisory-lock timeout, kill -9 mid-flight, lifecycleCtx
-- cancellation) the snapshot's total_turns/total_tokens/total_cost_usd is
-- silently off-by-one forever — feedback-closure guarantees cannot be claimed.
--
-- This migration introduces:
--   - session_aggregate_outbox (heap, NOT partitioned): one row per UpdateSession
--     payload, claimed+retried by a reaper in domains/session/v2/.
--   - The reaper is FOR UPDATE SKIP LOCKED so multiple gateway replicas can
--     cooperate without a single lock holder.
--   - Status state machine: pending → claimed → done | dead.
--   - Each outbox row carries the SessionUpdate payload as JSONB so the reaper
--     can replay without consulting session_turns (decoupling the snapshot
--     retry from the source-of-truth table that may itself be partitioned).
--
-- Idempotency:
--   - The aggregator's existing aggregate_applied_at claim is unchanged; the
--     reaper's UpdateSession call is a no-op when the claim is already set.
--   - When the reaper marks the outbox row done, the snapshot is guaranteed
--     to have advanced (or the original session had no row yet, which is the
--     fan-in legacy case — documented as best-effort, not closed-loop).
--
-- Backfill: none. Existing in-flight updates that lost their in-memory retry
-- are NOT retroactively repaired; the audit recommends running
-- SELECT recompute_session_aggregates() if the operator wants a one-shot
-- reconciliation, but that helper is not introduced here (out of scope).

BEGIN;

CREATE TABLE IF NOT EXISTS public.session_aggregate_outbox (
    id              bigserial PRIMARY KEY,
    tenant_id       text        NOT NULL,
    session_id      text        NOT NULL,
    partition_date  date        NOT NULL,
    request_id      text        NOT NULL,
    update_payload  jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'claimed', 'done', 'dead')),
    attempts        integer     NOT NULL DEFAULT 0,
    last_error      text,
    next_retry_at   timestamptz NOT NULL DEFAULT NOW(),
    claimed_at      timestamptz,
    completed_at    timestamptz,
    created_at      timestamptz NOT NULL DEFAULT NOW(),
    updated_at      timestamptz NOT NULL DEFAULT NOW(),
    CONSTRAINT session_aggregate_outbox_unique_request
        UNIQUE (tenant_id, session_id, partition_date, request_id)
);

CREATE INDEX IF NOT EXISTS idx_session_aggregate_outbox_pending
    ON public.session_aggregate_outbox (next_retry_at)
    WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_session_aggregate_outbox_dead
    ON public.session_aggregate_outbox (created_at DESC)
    WHERE status = 'dead';

CREATE INDEX IF NOT EXISTS idx_session_aggregate_outbox_session
    ON public.session_aggregate_outbox (tenant_id, session_id, partition_date);

-- audit-data-closure-C: tenant isolation on the outbox. Super-admin reads
-- (the reaper) explicitly bypass with the same set_config('app.bypass_rls')
-- pattern used by the provider-error aggregator.
ALTER TABLE public.session_aggregate_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.session_aggregate_outbox FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_session_aggregate_outbox ON public.session_aggregate_outbox;
CREATE POLICY tenant_isolation_session_aggregate_outbox ON public.session_aggregate_outbox
    USING (
        tenant_id = get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    )
    WITH CHECK (
        tenant_id = get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMIT;
