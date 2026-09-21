-- Migration: Add Request Type Classification Fields
-- Version: V3 Phase 1
-- Date: 2026-09-02
-- Purpose: Enable separation of client entry requests from outbound provider requests
-- Related: AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 7.1

BEGIN;

-- Add request classification fields to request_logs table
ALTER TABLE request_logs 
    ADD COLUMN IF NOT EXISTS request_type TEXT DEFAULT 'outbound',
    ADD COLUMN IF NOT EXISTS parent_request_id TEXT,
    ADD COLUMN IF NOT EXISTS request_depth INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS is_terminal BOOLEAN DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS task_tier TEXT,
    ADD COLUMN IF NOT EXISTS auto_decision JSONB,
    ADD COLUMN IF NOT EXISTS escalation_count INTEGER DEFAULT 0;

-- Add column comments for documentation
COMMENT ON COLUMN request_logs.request_type IS 
    'Request type: ''client'' (entry from client) or ''outbound'' (to provider)';
    
COMMENT ON COLUMN request_logs.parent_request_id IS 
    'Parent request ID for sub-agents and retries. Links child requests to parent.';
    
COMMENT ON COLUMN request_logs.request_depth IS 
    'Agent depth: 0=client entry, 1=first-level sub-agent, 2+=nested sub-agents';
    
COMMENT ON COLUMN request_logs.is_terminal IS 
    'TRUE if this is the final successful/failed attempt (stores response_body)';
    
COMMENT ON COLUMN request_logs.task_tier IS 
    'Tier classification: ''tier-a'' (high-perf), ''tier-b'' (standard), or ''tier-c'' (economy)';
    
COMMENT ON COLUMN request_logs.auto_decision IS 
    'Full auto-route decision metadata: candidates, scores, reasoning, task_type';
    
COMMENT ON COLUMN request_logs.escalation_count IS 
    'Number of tier escalations for this request (quality gate failures)';

-- Create indexes for new query patterns

-- Index for filtering by request type and time
CREATE INDEX IF NOT EXISTS idx_request_logs_request_type_ts 
    ON request_logs(request_type, ts DESC)
    WHERE request_type IS NOT NULL;

-- Index for parent-child relationship queries
CREATE INDEX IF NOT EXISTS idx_request_logs_parent_request_id 
    ON request_logs(parent_request_id) 
    WHERE parent_request_id IS NOT NULL;

-- Index for tier-based analytics
CREATE INDEX IF NOT EXISTS idx_request_logs_task_tier_ts 
    ON request_logs(task_tier, ts DESC) 
    WHERE task_tier IS NOT NULL;

-- Index for terminal request queries (cost/analytics)
CREATE INDEX IF NOT EXISTS idx_request_logs_terminal 
    ON request_logs(is_terminal, ts DESC) 
    WHERE is_terminal = TRUE;

-- Composite index for tier + terminal queries (cost per tier)
CREATE INDEX IF NOT EXISTS idx_request_logs_tier_terminal_ts
    ON request_logs(task_tier, is_terminal, ts DESC)
    WHERE task_tier IS NOT NULL AND is_terminal = TRUE;

-- Index for escalation analysis
CREATE INDEX IF NOT EXISTS idx_request_logs_escalation
    ON request_logs(escalation_count, task_tier, ts DESC)
    WHERE escalation_count > 0;

-- Backfill existing data (mark as outbound, terminal by default)
-- This preserves backward compatibility
UPDATE request_logs 
SET 
    request_type = 'outbound',
    is_terminal = TRUE,
    request_depth = 0
WHERE request_type IS NULL;

-- Verify the migration
DO $$
DECLARE
    v_count_type BIGINT;
    v_count_terminal BIGINT;
BEGIN
    -- Check that columns exist
    SELECT COUNT(*) INTO v_count_type
    FROM request_logs 
    WHERE request_type IS NOT NULL;
    
    SELECT COUNT(*) INTO v_count_terminal
    FROM request_logs 
    WHERE is_terminal IS NOT NULL;
    
    RAISE NOTICE 'Migration verification:';
    RAISE NOTICE '  - Records with request_type: %', v_count_type;
    RAISE NOTICE '  - Records with is_terminal: %', v_count_terminal;
    
    IF v_count_type = 0 THEN
        RAISE WARNING 'No records found with request_type set';
    END IF;
END $$;

COMMIT;

-- Expected output:
-- NOTICE: Migration verification:
-- NOTICE:   - Records with request_type: <count>
-- NOTICE:   - Records with is_terminal: <count>
