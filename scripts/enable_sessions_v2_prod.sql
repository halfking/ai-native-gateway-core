-- Script to enable sessions_v2 feature flags for PRODUCTION (154/245)
-- GRADUAL ROLLOUT: Start with 1%, increase after observation
--
-- Usage:
--   psql -h 154.xxx.xxx.xxx -U postgres -d llmgateway -f enable_sessions_v2_prod.sql
--   psql -h 245.xxx.xxx.xxx -U postgres -d llmgateway -f enable_sessions_v2_prod.sql
--
-- Rollout Timeline:
--   Day 1 (2026-07-24): 1% rollout
--   Day 2 (2026-07-25): 10% rollout (if no issues)
--   Day 3 (2026-07-26): 100% rollout (if no issues)

BEGIN;

-- Enable sessions_v2 master switch
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.enabled',
    'true',
    'bool',
    'platform',
    'session',
    'Sessions V2 总开关',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = 'true', updated_at = NOW();

-- Enable shadow write mode
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.shadow_write',
    'true',
    'bool',
    'platform',
    'session',
    '启用副写模式（同时写入request_logs和V2表）',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = 'true', updated_at = NOW();

-- PRODUCTION: Start with 1% rollout
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.rollout_percent',
    '1',
    'int',
    'platform',
    'session',
    'V2表的流量灰度百分比(0-100) - PRODUCTION GRADUAL ROLLOUT',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = '1', updated_at = NOW();

-- Set write timeout (conservative for production)
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.write_timeout_ms',
    '500',
    'int',
    'platform',
    'session',
    'V2写入超时时间(毫秒)',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = '500', updated_at = NOW();

-- Set read timeout
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.read_timeout_ms',
    '300',
    'int',
    'platform',
    'session',
    'V2读取超时时间(毫秒)',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = '300', updated_at = NOW();

-- Enable compression detection
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.compression_enabled',
    'true',
    'bool',
    'platform',
    'session',
    '启用V2压缩检测',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = 'true', updated_at = NOW();

-- Set turn logs retention (24 hours)
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.turn_logs_retention_hours',
    '24',
    'int',
    'platform',
    'session',
    '环节日志保留时间(小时)',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = '24', updated_at = NOW();

-- Verify settings
SELECT 
    key, 
    value, 
    type, 
    updated_at,
    CASE 
        WHEN key = 'sessions_v2.rollout_percent' THEN '⚠️  PRODUCTION: Starting with 1% rollout'
        ELSE 'OK'
    END as status
FROM platform_settings 
WHERE key LIKE 'sessions_v2.%'
ORDER BY key;

COMMIT;

-- After running this script:
-- 1. Monitor /api/admin/sessions/v2/shadow_metrics for 24 hours
-- 2. Check session_turns table for data: SELECT count(*) FROM gateway.session_turns WHERE ts > now() - interval '1 hour';
-- 3. If no errors, increase rollout_percent to 10:
--    UPDATE platform_settings SET value = '10' WHERE key = 'sessions_v2.rollout_percent';
-- 4. After another 24h, increase to 100:
--    UPDATE platform_settings SET value = '100' WHERE key = 'sessions_v2.rollout_percent';
