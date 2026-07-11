-- Migration 379: P1.4 Observability Hot Table Sync
--
-- Purpose: Add 10 P1.4 observability fields to both request_logs parent table
--          and request_logs_hot independent table, ensuring exact column match.
--
-- Background:
--   - Migration 341 created request_logs_hot as independent table using LIKE request_logs
--   - Commit 8d0c07ef0 added 10 P1.4 observability fields to Go INSERT statements
--   - Parent table request_logs lacks these 10 columns, causing INSERT failures
--   - Hot table request_logs_hot also lacks these 10 columns
--
-- This migration:
--   1. Adds 10 columns to request_logs parent (with IF NOT EXISTS safety)
--   2. Adds same 10 columns to request_logs_hot (with IF NOT EXISTS safety)
--   3. Creates indexes on hot table for operational queries
--   4. Recreates request_logs_with_current_month view if needed
--   5. Validates exact ordered column match between parent and hot
--
-- Dependencies:
--   - Migration 341 (hot table independence)
--   - Commit 8d0c07ef0 (Go code with 10-column INSERT)
--
-- Author: llm-gateway-ops (2026-07-11)

BEGIN;

-- ════════════════════════════════════════════════════════════
-- Part 1: Add P1.4 fields to request_logs PARENT table
-- ════════════════════════════════════════════════════════════

-- Caller metadata
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    client_ip INET;
COMMENT ON COLUMN request_logs.client_ip IS
'P1.4: Real client IP (X-Forwarded-For first / X-Real-IP / RemoteAddr)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    client_forwarded_for TEXT;
COMMENT ON COLUMN request_logs.client_forwarded_for IS
'P1.4: Full X-Forwarded-For header chain';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    agent_name VARCHAR(255);
COMMENT ON COLUMN request_logs.agent_name IS
'P1.4: Agent name: claude-code/opencode/cursor/vscode/postman/curl/unknown';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    agent_type VARCHAR(50);
COMMENT ON COLUMN request_logs.agent_type IS
'P1.4: Agent type: web/mobile/cli/api/bot/internal/unknown';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    api_key_fingerprint VARCHAR(16);
COMMENT ON COLUMN request_logs.api_key_fingerprint IS
'P1.4: First 8 chars of API key (e.g., sk-1234ab***)';

-- Session context
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    session_title TEXT;
COMMENT ON COLUMN request_logs.session_title IS
'P1.4: Human-readable session title from session manager';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    task_id VARCHAR(255);
COMMENT ON COLUMN request_logs.task_id IS
'P1.4: Task/work item ID (JIRA-123, GH-456, task_001)';

-- Routing metadata
ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    upstream_endpoint TEXT;
COMMENT ON COLUMN request_logs.upstream_endpoint IS
'P1.4: Full upstream URL (e.g., https://api.anthropic.com/v1/messages)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    upstream_protocol VARCHAR(50);
COMMENT ON COLUMN request_logs.upstream_protocol IS
'P1.4: Upstream protocol: openai/anthropic/gemini/glm/minimax/etc';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS
    protocol_conversion BOOLEAN;
COMMENT ON COLUMN request_logs.protocol_conversion IS
'P1.4: Whether protocol conversion occurred (OpenAI->Anthropic, etc)';

-- ════════════════════════════════════════════════════════════
-- Part 2: Add P1.4 fields to request_logs_hot (independent table)
-- ════════════════════════════════════════════════════════════

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    client_ip INET;
COMMENT ON COLUMN request_logs_hot.client_ip IS
'P1.4: Real client IP (X-Forwarded-For first / X-Real-IP / RemoteAddr)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    client_forwarded_for TEXT;
COMMENT ON COLUMN request_logs_hot.client_forwarded_for IS
'P1.4: Full X-Forwarded-For header chain';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    agent_name VARCHAR(255);
COMMENT ON COLUMN request_logs_hot.agent_name IS
'P1.4: Agent name: claude-code/opencode/cursor/vscode/postman/curl/unknown';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    agent_type VARCHAR(50);
COMMENT ON COLUMN request_logs_hot.agent_type IS
'P1.4: Agent type: web/mobile/cli/api/bot/internal/unknown';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    api_key_fingerprint VARCHAR(16);
COMMENT ON COLUMN request_logs_hot.api_key_fingerprint IS
'P1.4: First 8 chars of API key (e.g., sk-1234ab***)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    session_title TEXT;
COMMENT ON COLUMN request_logs_hot.session_title IS
'P1.4: Human-readable session title from session manager';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    task_id VARCHAR(255);
COMMENT ON COLUMN request_logs_hot.task_id IS
'P1.4: Task/work item ID (JIRA-123, GH-456, task_001)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    upstream_endpoint TEXT;
COMMENT ON COLUMN request_logs_hot.upstream_endpoint IS
'P1.4: Full upstream URL (e.g., https://api.anthropic.com/v1/messages)';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    upstream_protocol VARCHAR(50);
COMMENT ON COLUMN request_logs_hot.upstream_protocol IS
'P1.4: Upstream protocol: openai/anthropic/gemini/glm/minimax/etc';

ALTER TABLE request_logs_hot ADD COLUMN IF NOT EXISTS
    protocol_conversion BOOLEAN;
COMMENT ON COLUMN request_logs_hot.protocol_conversion IS
'P1.4: Whether protocol conversion occurred (OpenAI->Anthropic, etc)';

-- ════════════════════════════════════════════════════════════
-- Part 3: Indexes for query optimization (hot table only)
-- ════════════════════════════════════════════════════════════

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_client_ip
    ON request_logs_hot(client_ip) WHERE client_ip IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_agent_type
    ON request_logs_hot(agent_type) WHERE agent_type IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_protocol_conversion
    ON request_logs_hot(protocol_conversion) WHERE protocol_conversion = true;

-- ════════════════════════════════════════════════════════════
-- Part 4: Recreate request_logs_with_current_month view
-- ════════════════════════════════════════════════════════════
-- View uses SELECT * UNION ALL, which requires identical column order
-- between request_logs and request_logs_hot. Since we added columns,
-- we recreate the view to ensure it works.

DROP VIEW IF EXISTS request_logs_with_current_month CASCADE;

CREATE VIEW request_logs_with_current_month AS
SELECT * FROM request_logs_hot    -- Hot table (0-7 days, independent)
UNION ALL
SELECT * FROM request_logs;        -- Parent table (all ATTACHED monthly partitions)

COMMENT ON VIEW request_logs_with_current_month IS
'Optimized query VIEW using hot table architecture (migration 341).
Updated by migration 379 to support P1.4 observability fields.
- request_logs_hot: independent hot table (0-7 days) with 10 P1.4 columns
- request_logs: parent table (auto-aggregates all ATTACHED monthly partitions) with 10 P1.4 columns
PostgreSQL partition pruning applies to parent table queries.';

-- ════════════════════════════════════════════════════════════
-- Part 5: Validation - Exact ordered column match
-- ════════════════════════════════════════════════════════════

DO $$
DECLARE
    parent_cols TEXT[];
    hot_cols TEXT[];
    parent_types TEXT[];
    hot_types TEXT[];
    mismatch_cols TEXT[];
    missing_parent TEXT[];
    missing_hot TEXT[];
    expected_p14_cols TEXT[] := ARRAY[
        'client_ip', 'client_forwarded_for', 'agent_name', 'agent_type',
        'api_key_fingerprint', 'session_title', 'task_id',
        'upstream_endpoint', 'upstream_protocol', 'protocol_conversion'
    ];
    col TEXT;
    i INT;
BEGIN
    -- Get ordered column lists for both tables
    SELECT ARRAY_AGG(column_name ORDER BY ordinal_position),
           ARRAY_AGG(data_type ORDER BY ordinal_position)
    INTO parent_cols, parent_types
    FROM information_schema.columns
    WHERE table_name = 'request_logs'
      AND table_schema = 'public';

    SELECT ARRAY_AGG(column_name ORDER BY ordinal_position),
           ARRAY_AGG(data_type ORDER BY ordinal_position)
    INTO hot_cols, hot_types
    FROM information_schema.columns
    WHERE table_name = 'request_logs_hot'
      AND table_schema = 'public';

    -- Check array lengths match
    IF array_length(parent_cols, 1) != array_length(hot_cols, 1) THEN
        RAISE EXCEPTION 'Column count mismatch: parent has % columns, hot has %',
            array_length(parent_cols, 1), array_length(hot_cols, 1);
    END IF;

    -- Check column names and types match in order
    FOR i IN 1..array_length(parent_cols, 1) LOOP
        IF parent_cols[i] != hot_cols[i] THEN
            mismatch_cols := array_append(mismatch_cols,
                format('Position %: parent.%s vs hot.%s', i, parent_cols[i], hot_cols[i]));
        END IF;
        IF parent_types[i] != hot_types[i] THEN
            mismatch_cols := array_append(mismatch_cols,
                format('Type mismatch at %: parent.%s vs hot.%s',
                    parent_cols[i], parent_types[i], hot_types[i]));
        END IF;
    END LOOP;

    IF array_length(mismatch_cols, 1) > 0 THEN
        RAISE EXCEPTION 'Column order/type mismatch between parent and hot: %',
            array_to_string(mismatch_cols, '; ');
    END IF;

    -- Check all P1.4 columns exist in parent
    SELECT ARRAY_AGG(unnest) INTO missing_parent
    FROM unnest(expected_p14_cols)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'request_logs'
          AND table_schema = 'public'
          AND column_name = unnest
    );

    IF array_length(missing_parent, 1) > 0 THEN
        RAISE EXCEPTION 'request_logs parent missing P1.4 columns: %',
            array_to_string(missing_parent, ', ');
    END IF;

    -- Check all P1.4 columns exist in hot
    SELECT ARRAY_AGG(unnest) INTO missing_hot
    FROM unnest(expected_p14_cols)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'request_logs_hot'
          AND table_schema = 'public'
          AND column_name = unnest
    );

    IF array_length(missing_hot, 1) > 0 THEN
        RAISE EXCEPTION 'request_logs_hot missing P1.4 columns: %',
            array_to_string(missing_hot, ', ');
    END IF;

    -- Check view exists
    IF NOT EXISTS (
        SELECT 1 FROM pg_views
        WHERE viewname = 'request_logs_with_current_month'
          AND schemaname = 'public'
    ) THEN
        RAISE EXCEPTION 'VIEW request_logs_with_current_month was not created';
    END IF;

    -- Check indexes exist
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE tablename = 'request_logs_hot'
          AND indexname = 'idx_request_logs_hot_client_ip'
    ) THEN
        RAISE EXCEPTION 'Index idx_request_logs_hot_client_ip was not created';
    END IF;

    RAISE NOTICE '═══════════════════════════════════════════════════════════';
    RAISE NOTICE 'Migration 379: P1.4 Observability Hot Table Sync SUCCESSFUL';
    RAISE NOTICE '═══════════════════════════════════════════════════════════';
    RAISE NOTICE '✓ Added 10 P1.4 columns to request_logs parent table';
    RAISE NOTICE '✓ Added 10 P1.4 columns to request_logs_hot independent table';
    RAISE NOTICE '✓ Created 3 indexes on hot table for query optimization';
    RAISE NOTICE '✓ Recreated request_logs_with_current_month VIEW';
    RAISE NOTICE '✓ Validated exact column order match: % columns', array_length(parent_cols, 1);
    RAISE NOTICE '✓ Both tables now support commit 8d0c07ef0 INSERT statements';
    RAISE NOTICE '';
    RAISE NOTICE 'P1.4 columns: client_ip, client_forwarded_for, agent_name, agent_type,';
    RAISE NOTICE '              api_key_fingerprint, session_title, task_id,';
    RAISE NOTICE '              upstream_endpoint, upstream_protocol, protocol_conversion';
END $$;

COMMIT;
