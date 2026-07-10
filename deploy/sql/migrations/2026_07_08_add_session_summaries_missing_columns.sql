-- 2026-07-08: Add missing columns to session_summaries table for session analytics
-- These columns are referenced in session_analytics_handler.go and session_analytics_timeseries.go
-- but may not exist in production databases migrated from earlier versions

BEGIN;

-- Add health_score if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_summaries' AND column_name = 'health_score'
    ) THEN
        ALTER TABLE session_summaries ADD COLUMN health_score INTEGER;
        COMMENT ON COLUMN session_summaries.health_score IS 'Session health score (0-100)';
    END IF;
END $$;

-- Add health_grade if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_summaries' AND column_name = 'health_grade'
    ) THEN
        ALTER TABLE session_summaries ADD COLUMN health_grade VARCHAR(1);
        COMMENT ON COLUMN session_summaries.health_grade IS 'Session health grade (A, B, C, D, F)';
    END IF;
END $$;

-- Add range column if it doesn't exist (for session shape analytics)
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_summaries' AND column_name = 'range'
    ) THEN
        ALTER TABLE session_summaries ADD COLUMN range VARCHAR(20);
        COMMENT ON COLUMN session_summaries.range IS 'Session size range category (e.g., "1-5", "6-10", etc.)';
    END IF;
END $$;

-- Add last_health_at if it doesn't exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_name = 'session_summaries' AND column_name = 'last_health_at'
    ) THEN
        ALTER TABLE session_summaries ADD COLUMN last_health_at TIMESTAMP;
        COMMENT ON COLUMN session_summaries.last_health_at IS 'Timestamp of last health score calculation';
    END IF;
END $$;

-- Note: input_cost_usd and output_cost_usd already exist in session_summaries table
-- They were added in an earlier migration

COMMIT;
