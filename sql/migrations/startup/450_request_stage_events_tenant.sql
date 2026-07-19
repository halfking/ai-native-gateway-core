-- Migration: 450_request_stage_events_tenant
-- Purpose: request_stage_events 增加租户归属，保证规范化链路事件可按租户隔离。
-- Date: 2026-07-20
-- Idempotent: YES

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.request_stage_events') IS NOT NULL THEN
        ALTER TABLE public.request_stage_events
            ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT 'default';
        CREATE INDEX IF NOT EXISTS idx_stage_events_tenant_ts
            ON public.request_stage_events (tenant_id, event_timestamp DESC);
    END IF;
END $$;

COMMIT;
