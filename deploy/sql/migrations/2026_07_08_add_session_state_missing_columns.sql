-- 2026-07-08: Add missing columns to session_state table for session analytics
-- These columns are referenced in session_analytics_handler.go and session_analytics_timeseries.go
-- but may not exist in production databases migrated from earlier versions

BEGIN;

-- Add input_cost_usd and output_cost_usd if they don't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'input_cost_usd'
    ) THEN
        ALTER TABLE session_state ADD COLUMN input_cost_usd NUMERIC(12,6) DEFAULT 0.0;
        COMMENT ON COLUMN session_state.input_cost_usd IS 'Input/prompt token cost in USD';
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'output_cost_usd'
    ) THEN
        ALTER TABLE session_state ADD COLUMN output_cost_usd NUMERIC(12,6) DEFAULT 0.0;
        COMMENT ON COLUMN session_state.output_cost_usd IS 'Output/completion token cost in USD';
    END IF;
END $$;

-- Add health_score if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'health_score'
    ) THEN
        ALTER TABLE session_state ADD COLUMN health_score INTEGER;
        COMMENT ON COLUMN session_state.health_score IS 'Session health score (0-100)';
    END IF;
END $$;

-- Add health_grade if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'health_grade'
    ) THEN
        ALTER TABLE session_state ADD COLUMN health_grade VARCHAR(1);
        COMMENT ON COLUMN session_state.health_grade IS 'Session health grade (A, B, C, D, F)';
    END IF;
END $$;

-- Add range column if it doesn't exist (for session shape analytics)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'range'
    ) THEN
        ALTER TABLE session_state ADD COLUMN range VARCHAR(20);
        COMMENT ON COLUMN session_state.range IS 'Session size range category (e.g., "1-5", "6-10", etc.)';
    END IF;
END $$;

-- Add last_health_at if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_state' AND column_name = 'last_health_at'
    ) THEN
        ALTER TABLE session_state ADD COLUMN last_health_at TIMESTAMP;
        COMMENT ON COLUMN session_state.last_health_at IS 'Timestamp of last health score calculation';
    END IF;
END $$;

COMMIT;
