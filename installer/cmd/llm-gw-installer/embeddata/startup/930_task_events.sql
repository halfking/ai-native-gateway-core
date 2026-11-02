-- ===========================================================================
-- File:          sql/migrations/startup/930_task_events.sql
-- Migration:     930
-- Purpose:       taskbus 的 task_events 表（P1-1 S1 后半，ADR
--                docs/adr/2026-11-02-outbox-consolidation-via-taskbus.md）。
--                目标表形态与 internal/taskbus/doc.go 的规划一致；列语义
--                复刻 MemoryStore：pending/failed 且 next_retry_at 到期可
--                claim；sent/dlq 是终态（回收窗口的基准 = completed_at）。
--
-- Down: 930_task_events.down.sql
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS public.task_events (
    id              BIGSERIAL PRIMARY KEY,
    task_type       TEXT NOT NULL,
    payload         BYTEA NOT NULL,
    payload_version INT NOT NULL DEFAULT 1,
    idempotency_key TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INT NOT NULL DEFAULT 0,
    next_retry_at   TIMESTAMPTZ,
    last_error      TEXT,
    claimed_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ,
    CONSTRAINT uq_task_events_idem UNIQUE (task_type, idempotency_key),
    CONSTRAINT ck_task_events_status CHECK (status IN ('pending','failed','dlq','sent'))
);
CREATE INDEX IF NOT EXISTS idx_task_events_claim
    ON public.task_events (status, next_retry_at)
    WHERE status IN ('pending','failed');

INSERT INTO public.schema_migrations (version, description)
VALUES ('930', 'taskbus task_events table (P1-1 S1: idempotent enqueue via (task_type,idempotency_key) unique, claim index on pending/failed + next_retry_at, completed_at as retention base)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
