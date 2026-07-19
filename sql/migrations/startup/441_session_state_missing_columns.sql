-- 2026-07-08: Add missing columns to session_state table for session analytics
-- These columns are referenced in session_analytics_handler.go and session_analytics_timeseries.go
-- but may not exist in production databases migrated from earlier versions.
--
-- Idempotent: if public.session_state does not exist (legacy/analytics-optional
-- deployments), the whole migration is a no-op so deploy can proceed.

BEGIN;

DO $$
BEGIN
    IF to_regclass('public.session_state') IS NULL THEN
        RAISE NOTICE '441: public.session_state does not exist — skip column backfill';
        RETURN;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'input_cost_usd'
    ) THEN
        ALTER TABLE session_state ADD COLUMN input_cost_usd NUMERIC(12,6) DEFAULT 0.0;
        COMMENT ON COLUMN session_state.input_cost_usd IS 'Input/prompt token cost in USD';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'output_cost_usd'
    ) THEN
        ALTER TABLE session_state ADD COLUMN output_cost_usd NUMERIC(12,6) DEFAULT 0.0;
        COMMENT ON COLUMN session_state.output_cost_usd IS 'Output/completion token cost in USD';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'health_score'
    ) THEN
        ALTER TABLE session_state ADD COLUMN health_score INTEGER;
        COMMENT ON COLUMN session_state.health_score IS 'Session health score (0-100)';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'health_grade'
    ) THEN
        ALTER TABLE session_state ADD COLUMN health_grade VARCHAR(1);
        COMMENT ON COLUMN session_state.health_grade IS 'Session health grade (A, B, C, D, F)';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'range'
    ) THEN
        ALTER TABLE session_state ADD COLUMN range VARCHAR(20);
        COMMENT ON COLUMN session_state.range IS 'Session size range category (e.g., "1-5", "6-10", etc.)';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public' AND table_name = 'session_state' AND column_name = 'last_health_at'
    ) THEN
        ALTER TABLE session_state ADD COLUMN last_health_at TIMESTAMP;
        COMMENT ON COLUMN session_state.last_health_at IS 'Timestamp of last health score calculation';
    END IF;
END $$;

COMMIT;
