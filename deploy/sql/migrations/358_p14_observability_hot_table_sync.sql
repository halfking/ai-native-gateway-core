-- Migration 358: P1.4 Observability Hot Table Sync
-- 
-- Purpose: Synchronize request_logs_hot with parent table observability fields.
--          Parent table already has 26 observability fields from 2026-07-11-observability-fields.sql.
--          This migration adds the 10 P1.4-authoritative fields to request_logs_hot only.
--
-- Scope: request_logs_hot independent table (does NOT inherit from parent).
--        Ensures INSERT INTO request_logs_hot column list matches parent schema.
--
-- Dependencies: Requires 2026-07-11-observability-fields.sql applied to parent table first.

BEGIN;

-- ────────────────────────────────────────────────────────────────
-- Part 1: Add P1.4 fields to request_logs_hot (match parent types)
-- ────────────────────────────────────────────────────────────────

-- Caller metadata
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    client_ip INET;
COMMENT ON COLUMN request_logs_hot.client_ip IS 'Real client IP (X-Forwarded-For first / X-Real-IP / RemoteAddr)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    client_forwarded_for TEXT;
COMMENT ON COLUMN request_logs_hot.client_forwarded_for IS 'Full X-Forwarded-For header chain (matches parent TEXT type)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    agent_name VARCHAR(255);
COMMENT ON COLUMN request_logs_hot.agent_name IS 'Agent name: claude-code/opencode/cursor/vscode/postman/curl/unknown';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    agent_type VARCHAR(50);
COMMENT ON COLUMN request_logs_hot.agent_type IS 'Agent type: web/mobile/cli/api/bot/internal/unknown';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    api_key_fingerprint VARCHAR(16);
COMMENT ON COLUMN request_logs_hot.api_key_fingerprint IS 'First 8 chars of API key (e.g., sk-1234ab***)';

-- Session context
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    session_title TEXT;
COMMENT ON COLUMN request_logs_hot.session_title IS 'Human-readable session title from session manager';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    task_id VARCHAR(255);
COMMENT ON COLUMN request_logs_hot.task_id IS 'Task/work item ID (JIRA-123, GH-456, task_001)';

-- Routing metadata
ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    upstream_endpoint TEXT;
COMMENT ON COLUMN request_logs_hot.upstream_endpoint IS 'Full upstream URL (e.g., https://api.anthropic.com/v1/messages)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    upstream_protocol VARCHAR(50);
COMMENT ON COLUMN request_logs_hot.upstream_protocol IS 'Upstream protocol: openai/anthropic/gemini/glm/minimax/etc';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS 
    protocol_conversion BOOLEAN;
COMMENT ON COLUMN request_logs_hot.protocol_conversion IS 'Whether protocol conversion occurred (OpenAI->Anthropic, etc)';

-- ────────────────────────────────────────────────────────────────
-- Part 2: Indexes for query optimization (hot table only)
-- ────────────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_client_ip 
    ON request_logs_hot(client_ip) WHERE client_ip IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_agent_type 
    ON request_logs_hot(agent_type) WHERE agent_type IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_protocol_conversion 
    ON request_logs_hot(protocol_conversion) WHERE protocol_conversion = true;

-- ────────────────────────────────────────────────────────────────
-- Part 3: Validation
-- ────────────────────────────────────────────────────────────────

DO $$
DECLARE
    missing_hot TEXT[];
    expected_cols TEXT[] := ARRAY[
        'client_ip', 'client_forwarded_for', 'agent_name', 'agent_type',
        'api_key_fingerprint', 'session_title', 'task_id',
        'upstream_endpoint', 'upstream_protocol', 'protocol_conversion'
    ];
    col TEXT;
BEGIN
    -- Check hot table
    SELECT ARRAY_AGG(unnest) INTO missing_hot
    FROM unnest(expected_cols)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'request_logs_hot' AND column_name = unnest
    );
    
    IF array_length(missing_hot, 1) > 0 THEN
        RAISE EXCEPTION 'request_logs_hot missing columns: %', array_to_string(missing_hot, ', ');
    END IF;
    
    RAISE NOTICE '✓ Migration 358: Added 10 observability fields to request_logs_hot';
    RAISE NOTICE '✓ Created 3 indexes for query optimization';
    RAISE NOTICE '✓ request_logs_hot now matches parent table observability schema';
END $$;

COMMIT;
