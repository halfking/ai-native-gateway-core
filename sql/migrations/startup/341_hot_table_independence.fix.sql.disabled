-- Migration 341-fix: 修复 request_logs_hot 缺失列
-- Created: 2026-07-08
-- Purpose: 如果 request_logs_hot 在 migration 054/320/325 之前创建，
--          需要补充这些缺失的列。

BEGIN;

-- 1. 补充 migration 054 的列
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS client_request_id TEXT;

-- 2. 补充 migration 320 的列
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS upstream_status_code INT,
    ADD COLUMN IF NOT EXISTS client_timeout BOOLEAN,
    ADD COLUMN IF NOT EXISTS client_endpoint TEXT,
    ADD COLUMN IF NOT EXISTS stream_chunk_errors INT,
    ADD COLUMN IF NOT EXISTS stream_chunks_sent INT NOT NULL DEFAULT 0;

-- 3. 补充 migration 325 的列
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS attachments JSONB;

-- 4. 添加缺失的索引
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_client_request_id
    ON public.request_logs_hot (client_request_id, ts DESC)
    WHERE client_request_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_has_attachments
    ON public.request_logs_hot (ts DESC)
    WHERE attachments IS NOT NULL;

-- 5. 验证
DO $$
DECLARE
    col_count INT;
BEGIN
    SELECT count(*) INTO col_count
    FROM information_schema.columns
    WHERE table_name = 'request_logs_hot'
    AND column_name IN (
        'client_request_id',
        'upstream_status_code',
        'client_timeout',
        'client_endpoint',
        'stream_chunk_errors',
        'stream_chunks_sent',
        'attachments'
    );
    
    IF col_count < 7 THEN
        RAISE WARNING 'Only % of 7 expected columns found in request_logs_hot', col_count;
    ELSE
        RAISE NOTICE 'All 7 columns verified in request_logs_hot';
    END IF;
END $$;

COMMIT;

\echo ''
\echo 'Migration 341-fix complete:'
\echo '  ✅ Added missing columns to request_logs_hot'
\echo '  ✅ Added missing indexes'
\echo ''
