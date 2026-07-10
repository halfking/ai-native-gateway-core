-- Migration: 2026-07-11 Observability Fields Extension
-- Purpose: Add comprehensive tracing fields for caller, session, security, and vendor metadata
-- Scope: request_logs parent table (applies to all partitions)

BEGIN;

-- ────────────────────────────────────────────────────────────────
-- Caller Information
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    client_ip INET;
COMMENT ON COLUMN request_logs.client_ip IS 'Client real IP (extracted from X-Forwarded-For or X-Real-IP)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    client_forwarded_for TEXT;
COMMENT ON COLUMN request_logs.client_forwarded_for IS 'Full X-Forwarded-For header chain';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    agent_name VARCHAR(255);
COMMENT ON COLUMN request_logs.agent_name IS 'Agent/application name (e.g., claude-code, opencode, custom-bot)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    agent_type VARCHAR(50);
COMMENT ON COLUMN request_logs.agent_type IS 'Agent type: web/mobile/cli/api/bot/internal';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    api_key_fingerprint VARCHAR(16);
COMMENT ON COLUMN request_logs.api_key_fingerprint IS 'First 8 chars of API key for masking (e.g., sk-1234ab***)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    customer_id BIGINT;
COMMENT ON COLUMN request_logs.customer_id IS 'Customer/organization ID for multi-tenant billing';

-- ────────────────────────────────────────────────────────────────
-- Provider Detail
-- ────────────────────────────────────────────────────────────────

-- credential_id already exists in schema (line 15)
-- Just add upstream_endpoint

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    upstream_endpoint TEXT;
COMMENT ON COLUMN request_logs.upstream_endpoint IS 'Full upstream API endpoint URL (e.g., https://api.anthropic.com/v1/messages)';

-- ────────────────────────────────────────────────────────────────
-- Session & Task Context
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    session_title TEXT;
COMMENT ON COLUMN request_logs.session_title IS 'Human-readable session title (from session manager)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    session_summary TEXT;
COMMENT ON COLUMN request_logs.session_summary IS 'Session summary/description';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    task_id VARCHAR(255);
COMMENT ON COLUMN request_logs.task_id IS 'Task/work item ID (e.g., JIRA-123, task_001)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    task_title TEXT;
COMMENT ON COLUMN request_logs.task_title IS 'Task title/description';

-- task_type already exists in schema (line 63)
-- No need to add again

-- ────────────────────────────────────────────────────────────────
-- Compression & Optimization
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    compression_start_index INT;
COMMENT ON COLUMN request_logs.compression_start_index IS 'Starting message index for context compression';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    compression_end_index INT;
COMMENT ON COLUMN request_logs.compression_end_index IS 'Ending message index for context compression';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    compression_ratio FLOAT;
COMMENT ON COLUMN request_logs.compression_ratio IS 'Compression ratio (compressed_tokens / original_tokens)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    cache_hit BOOLEAN;
COMMENT ON COLUMN request_logs.cache_hit IS 'Whether request hit cache (semantic/exact match)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    cache_tokens_saved INT;
COMMENT ON COLUMN request_logs.cache_tokens_saved IS 'Tokens saved due to cache hit';

-- ────────────────────────────────────────────────────────────────
-- Security & Compliance
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    content_safety_score JSONB;
COMMENT ON COLUMN request_logs.content_safety_score IS 'Content safety analysis (e.g., {"score": 0.95, "categories": {"hate": 0.01}})';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    dlp_violations JSONB;
COMMENT ON COLUMN request_logs.dlp_violations IS 'DLP violation details (e.g., [{"type": "ssn", "count": 1}])';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    sensitive_keywords TEXT[];
COMMENT ON COLUMN request_logs.sensitive_keywords IS 'Matched sensitive keywords (e.g., ["password", "secret"])';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    rate_limit_status VARCHAR(50);
COMMENT ON COLUMN request_logs.rate_limit_status IS 'Rate limit status: under_limit/approaching_limit/exceeded/bypassed';

-- ────────────────────────────────────────────────────────────────
-- Protocol & Conversion Metadata
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    client_protocol VARCHAR(50);
COMMENT ON COLUMN request_logs.client_protocol IS 'Client protocol (e.g., openai, anthropic, gemini)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    upstream_protocol VARCHAR(50);
COMMENT ON COLUMN request_logs.upstream_protocol IS 'Upstream provider protocol (e.g., anthropic, openai, bedrock)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    protocol_conversion BOOLEAN;
COMMENT ON COLUMN request_logs.protocol_conversion IS 'Whether protocol conversion was performed (OpenAI -> Anthropic, etc.)';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    ir_extensions JSONB;
COMMENT ON COLUMN request_logs.ir_extensions IS 'IR (intermediate representation) extension fields (e.g., {"reasoning_effort": "medium"})';

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    sanitizer_mutations JSONB;
COMMENT ON COLUMN request_logs.sanitizer_mutations IS 'Sanitizer mutations applied (e.g., {"stripped_fields": ["user_metadata"]})';

-- ────────────────────────────────────────────────────────────────
-- Vendor Metadata
-- ────────────────────────────────────────────────────────────────

ALTER TABLE request_logs ADD COLUMN IF NOT EXISTS 
    vendor_metadata JSONB;
COMMENT ON COLUMN request_logs.vendor_metadata IS 'Vendor-specific fields snapshot (e.g., {"reasoning_tokens": 1500, "provider_request_id": "req_abc"})';

-- ────────────────────────────────────────────────────────────────
-- Indexes for Query Optimization
-- ────────────────────────────────────────────────────────────────

CREATE INDEX IF NOT EXISTS idx_request_logs_client_ip 
    ON request_logs(client_ip);

CREATE INDEX IF NOT EXISTS idx_request_logs_customer_id 
    ON request_logs(customer_id);

CREATE INDEX IF NOT EXISTS idx_request_logs_task_type 
    ON request_logs(task_type);

CREATE INDEX IF NOT EXISTS idx_request_logs_protocol_conversion 
    ON request_logs(protocol_conversion) 
    WHERE protocol_conversion = true;

CREATE INDEX IF NOT EXISTS idx_request_logs_rate_limit_status 
    ON request_logs(rate_limit_status) 
    WHERE rate_limit_status IN ('exceeded', 'approaching_limit');

CREATE INDEX IF NOT EXISTS idx_request_logs_agent_type 
    ON request_logs(agent_type);

-- ────────────────────────────────────────────────────────────────
-- Validation
-- ────────────────────────────────────────────────────────────────

DO $$
DECLARE
    missing_cols TEXT[];
BEGIN
    -- Check all expected columns exist
    SELECT ARRAY_AGG(col) INTO missing_cols
    FROM (VALUES 
        ('client_ip'), ('client_forwarded_for'), ('agent_name'), ('agent_type'),
        ('api_key_fingerprint'), ('customer_id'), ('upstream_endpoint'),
        ('session_title'), ('session_summary'), ('task_id'), ('task_title'),
        ('compression_start_index'), ('compression_end_index'), ('compression_ratio'),
        ('cache_hit'), ('cache_tokens_saved'),
        ('content_safety_score'), ('dlp_violations'), ('sensitive_keywords'), ('rate_limit_status'),
        ('client_protocol'), ('upstream_protocol'), ('protocol_conversion'),
        ('ir_extensions'), ('sanitizer_mutations'), ('vendor_metadata')
    ) AS expected(col)
    WHERE NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'request_logs' AND column_name = expected.col
    );
    
    IF array_length(missing_cols, 1) > 0 THEN
        RAISE EXCEPTION 'Missing columns: %', array_to_string(missing_cols, ', ');
    END IF;
    
    RAISE NOTICE 'Migration 2026-07-11-observability-fields completed successfully';
    RAISE NOTICE 'Added 26 new observability fields and 6 indexes';
END $$;

COMMIT;
