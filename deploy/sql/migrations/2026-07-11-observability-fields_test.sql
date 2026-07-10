-- Test: 2026-07-11 Observability Fields Migration
-- Purpose: Verify all new fields can be inserted and queried correctly

BEGIN;

-- Clean test data
DELETE FROM request_logs WHERE request_id LIKE 'test_obs_%';

-- Test 1: Insert row with all new observability fields
INSERT INTO request_logs (
    request_id, ts, tenant_id, success,
    -- New observability fields
    client_ip, client_forwarded_for, agent_name, agent_type,
    api_key_fingerprint, customer_id,
    credential_id, upstream_endpoint,
    session_title, session_summary, task_id, task_title, task_type,
    compression_start_index, compression_end_index, compression_ratio,
    cache_hit, cache_tokens_saved,
    content_safety_score, dlp_violations, sensitive_keywords, rate_limit_status,
    client_protocol, upstream_protocol, protocol_conversion,
    ir_extensions, sanitizer_mutations, vendor_metadata
) VALUES (
    'test_obs_001', NOW(), 'test_tenant', true,
    '192.168.1.100'::INET, '10.0.0.1, 192.168.1.100', 'claude-code', 'cli',
    'sk-1234abcd***', 10001,
    2001, 'https://api.anthropic.com/v1/messages',
    'Feature Implementation', 'Implementing observability fields', 'task_001', 'P2.1 Observability', 'feature',
    50, 100, 0.35,
    true, 5000,
    '{"score": 0.95, "categories": {"hate": 0.01}}'::jsonb,
    '[{"type": "ssn", "count": 1}]'::jsonb,
    ARRAY['password', 'secret'],
    'under_limit',
    'openai', 'anthropic', true,
    '{"reasoning_effort": "medium"}'::jsonb,
    '{"stripped_fields": ["user_metadata"]}'::jsonb,
    '{"reasoning_tokens": 1500, "provider_request_id": "req_abc123"}'::jsonb
);

-- Test 2: Verify data can be read back correctly
DO $$
DECLARE
    v_client_ip INET;
    v_agent_name VARCHAR(255);
    v_compression_ratio FLOAT;
    v_protocol_conversion BOOLEAN;
    v_vendor_meta JSONB;
BEGIN
    SELECT 
        client_ip, agent_name, compression_ratio, protocol_conversion, vendor_metadata
    INTO 
        v_client_ip, v_agent_name, v_compression_ratio, v_protocol_conversion, v_vendor_meta
    FROM request_logs
    WHERE request_id = 'test_obs_001';
    
    -- Assertions
    IF v_client_ip != '192.168.1.100'::INET THEN
        RAISE EXCEPTION 'client_ip mismatch: expected 192.168.1.100, got %', v_client_ip;
    END IF;
    
    IF v_agent_name != 'claude-code' THEN
        RAISE EXCEPTION 'agent_name mismatch: expected claude-code, got %', v_agent_name;
    END IF;
    
    IF v_compression_ratio != 0.35 THEN
        RAISE EXCEPTION 'compression_ratio mismatch: expected 0.35, got %', v_compression_ratio;
    END IF;
    
    IF v_protocol_conversion != true THEN
        RAISE EXCEPTION 'protocol_conversion mismatch: expected true, got %', v_protocol_conversion;
    END IF;
    
    IF (v_vendor_meta->>'reasoning_tokens')::INT != 1500 THEN
        RAISE EXCEPTION 'vendor_metadata reasoning_tokens mismatch';
    END IF;
    
    RAISE NOTICE 'All observability fields verified successfully';
END $$;

-- Test 3: Verify indexes exist
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE tablename = 'request_logs' AND indexname = 'idx_request_logs_client_ip'
    ) THEN
        RAISE EXCEPTION 'Index idx_request_logs_client_ip not found';
    END IF;
    
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE tablename = 'request_logs' AND indexname = 'idx_request_logs_customer_id'
    ) THEN
        RAISE EXCEPTION 'Index idx_request_logs_customer_id not found';
    END IF;
    
    RAISE NOTICE 'All indexes verified';
END $$;

-- Cleanup
DELETE FROM request_logs WHERE request_id LIKE 'test_obs_%';

ROLLBACK;
