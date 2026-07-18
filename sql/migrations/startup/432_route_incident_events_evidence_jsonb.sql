-- 432_route_incident_events_evidence_jsonb.sql
-- 2026-07-19: 确保 route_incident_events.evidence 列为 JSONB
--
-- 背景:
--   389 迁移定义 evidence JSONB NOT NULL DEFAULT '{}'::jsonb，但在某些环境
--   中列类型为 TEXT，导致 json.Marshal([]byte) 写入时报错:
--   "invalid input syntax for type json (SQLSTATE 22P02)"
--
--   根本原因可能是 389 迁移的 CREATE TABLE IF NOT EXISTS 在旧表已存在时
--   (例如 TEXT 类型创建于 389 之前的手动建表) 不会修改已有列。
--
--   本迁移使用幂等方式确保列类型为 JSONB，并修复 Go 客户端传参方式
--   (已同步在 store.go 中使用 $N::jsonb 显式类型转换)。

BEGIN;

-- 安全地将 evidence 列转为 JSONB (如果还不是)
-- 对已有 TEXT 数据，使用 ::jsonb 进行类型转换
DO $$
BEGIN
    -- 检查列是否存在且不是 JSONB 类型
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'route_incident_events'
          AND column_name = 'evidence'
          AND data_type NOT IN ('json', 'jsonb')
    ) THEN
        ALTER TABLE route_incident_events
            ALTER COLUMN evidence TYPE JSONB
            USING evidence::jsonb;
        RAISE NOTICE 'route_incident_events.evidence converted to JSONB';
    ELSE
        RAISE NOTICE 'route_incident_events.evidence already JSONB, skipping';
    END IF;
END $$;

-- 如果列是 JSON 而非 JSONB (极少见)，也转为 JSONB
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'route_incident_events'
          AND column_name = 'evidence'
          AND data_type = 'json'
    ) THEN
        ALTER TABLE route_incident_events
            ALTER COLUMN evidence TYPE JSONB
            USING evidence::jsonb;
        RAISE NOTICE 'route_incident_events.evidence converted from json to jsonb';
    END IF;
END $$;

-- 确保 NOT NULL 约束存在
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'route_incident_events'
          AND column_name = 'evidence'
          AND is_nullable = 'YES'
    ) THEN
        ALTER TABLE route_incident_events
            ALTER COLUMN evidence SET NOT NULL;
        RAISE NOTICE 'route_incident_events.evidence NOT NULL constraint added';
    END IF;
END $$;

-- 确保默认值存在
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'route_incident_events'
          AND column_name = 'evidence'
          AND column_default IS NOT NULL
    ) THEN
        ALTER TABLE route_incident_events
            ALTER COLUMN evidence SET DEFAULT '{}'::jsonb;
        RAISE NOTICE 'route_incident_events.evidence DEFAULT set';
    END IF;
END $$;

COMMIT;
