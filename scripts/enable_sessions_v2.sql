-- Script to enable sessions_v2 feature flags
-- Run this on 154, 245, and local environments
--
-- Usage:
--   psql -h <host> -U <user> -d <database> -f enable_sessions_v2.sql
--
-- Phase 1: Shadow write (1% rollout for validation)
-- Phase 2: Increase to 100% rollout after 24h observation
-- Phase 3: Enable l3_read for cache testing

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

-- Set rollout percentage (start with 100% for immediate full rollout)
-- For production 154/245: start with 1, increase gradually
INSERT INTO platform_settings (key, value, type, scope, category, description, updated_at)
VALUES (
    'sessions_v2.rollout_percent',
    '100',
    'int',
    'platform',
    'session',
    'V2表的流量灰度百分比(0-100)',
    NOW()
)
ON CONFLICT (key) DO UPDATE
SET value = '100', updated_at = NOW();

-- Set write timeout
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
SELECT key, value, type, updated_at 
FROM platform_settings 
WHERE key LIKE 'sessions_v2.%'
ORDER BY key;

COMMIT;

-- Notes:
-- 1. For local env: Run with rollout_percent=100
-- 2. For 154/245 prod: 
--    - Day 1: rollout_percent=1 (1% traffic)
--    - Day 2: rollout_percent=10 (10% traffic)
--    - Day 3: rollout_percent=100 (full rollout)
-- 3. Monitor metrics at /api/admin/sessions/v2/status
