-- Migration 034: Add concurrency_limit_auto column for auto-tuning
-- Created: 2026-06-22
-- Purpose: Store algorithm-recommended concurrency limit (separate from manual override)

ALTER TABLE credentials
ADD COLUMN IF NOT EXISTS concurrency_limit_auto INT;

-- Index for auto-scaleup worker queries
CREATE INDEX IF NOT EXISTS idx_credentials_auto_limit 
    ON credentials (concurrency_limit_auto)
    WHERE concurrency_limit_auto IS NOT NULL;

-- Comments
COMMENT ON COLUMN credentials.concurrency_limit_auto IS 
    'Algorithm-recommended concurrency limit. Adjusted dynamically based on 429/503 errors and success rate. Read priority: concurrency_limit (manual) > concurrency_limit_auto > default 5.';

-- Update existing rows: initialize auto limit from manual limit
UPDATE credentials
SET concurrency_limit_auto = COALESCE(concurrency_limit, 5)
WHERE concurrency_limit_auto IS NULL;
