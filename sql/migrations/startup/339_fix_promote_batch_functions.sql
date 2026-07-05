-- Migration 339: 修复 promote_*_default_batch 函数的语法错误
--
-- 问题根因：
--   Migration 336 中的 promote_*_default_batch 函数使用了
--   DELETE FROM ... WHERE ... ORDER BY ts LIMIT p_batch_size RETURNING *
--   的语法，但 PostgreSQL 不支持在 DELETE 语句中使用 ORDER BY 和 LIMIT。
--
--   症状：
--     应用 migration 336 时，整个事务回滚，所有 8 个 promote_*_default_batch
--     函数都没有创建成功：
--       psql:db/migrations/336_...sql:118: ERROR:  syntax error at or near "ORDER"
--
--   影响：
--     - bg/partition_manager.go::promoteDefaultToPartitions() 调用这些函数时会
--       报"function does not exist"错误，每小时重复触发。
--     - *_default 表的数据永远不会被迁移到月度分区，导致 *_default 无限增长。
--
-- 解决方案：
--   使用两步法（SELECT + DELETE），避免 DELETE 中使用 ORDER BY/LIMIT：
--     1. 先 SELECT 找出需要迁移的行（带 ORDER BY + LIMIT）
--     2. 再用 IN (SELECT FROM CTE) DELETE 原表
--     3. 最后 INSERT 到父表（PG 会按 ts 自动路由到对应月度分区）
--
-- 适用表：
--   request_logs, request_wal, usage_ledger, routing_decision_log,
--   credential_model_index, request_logs_bodies, credit_ledger, tool_usage_stats
--
-- 注意：
--   request_logs_bodies 的 monthly partitions 是 columnar（migration 328a），
--   迁移到这些 columnar 分区的行无法再 UPDATE/DELETE，这正是设计意图
--   （历史数据只读）。其他表的月度分区是 heap（migration 333, 335 设计如此），
--   理论上仍可 UPDATE/DELETE，但架构上假定迁移后的数据不再修改。

BEGIN;

-- ============================================================
-- 通用辅助函数：删除已存在的函数（从 migration 336 的失败回滚中清理）
-- ============================================================
DO $$
DECLARE
    func_names text[] := ARRAY[
        'promote_request_logs_default_batch',
        'promote_request_wal_default_batch',
        'promote_usage_ledger_default_batch',
        'promote_routing_decision_log_default_batch',
        'promote_credential_model_index_default_batch',
        'promote_request_logs_bodies_default_batch',
        'promote_credit_ledger_default_batch',
        'promote_tool_usage_stats_default_batch'
    ];
    fn text;
BEGIN
    FOREACH fn IN ARRAY func_names LOOP
        EXECUTE format('DROP FUNCTION IF EXISTS public.%I(interval, int)', fn);
    END LOOP;
    RAISE NOTICE 'Dropped any partial functions from failed migration 336';
END
$$;

-- ============================================================
-- 1. promote_request_logs_default_batch
-- ============================================================
-- request_logs 是 RANGE(ts) 分区表。当月分区已被 migration 337 DETACH。
-- 此函数将 request_logs_default 中超过保留窗口（默认 7 天）的行迁移到
-- 对应的月度分区。

CREATE OR REPLACE FUNCTION promote_request_logs_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    -- Step 1: 创建临时表保存需要迁移的行
    CREATE TEMP TABLE _promote_rl_batch ON COMMIT DROP AS
    SELECT * FROM public.request_logs_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    -- Step 2: 从 default 表删除这些行
    DELETE FROM public.request_logs_default
    WHERE id IN (SELECT id FROM _promote_rl_batch);
    
    -- Step 3: 插入到父表，PG 按 ts 自动路由到对应月度分区
    -- （注意：月度分区已 DETACH，需要 ATTACH 后才能路由，但 ATTACH 不在
    -- 本函数职责范围内，由 partition_manager 的 ensure 函数负责）
    BEGIN
        INSERT INTO public.request_logs
        SELECT * FROM _promote_rl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        -- 如果 ATTACH 失败，保留在 default 表里（不丢失数据）
        RAISE WARNING 'promote_request_logs_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_logs_default_batch(interval, int) IS
'Move one batch of cold rows (older than p_retention, default 7 days) from
request_logs_default into the matching monthly partition (via parent insert).
Returns rows moved. Iterate until 0 to drain.
Fixed 2026-07-04 in migration 339 (was using illegal DELETE ORDER BY LIMIT).';

-- ============================================================
-- 2. promote_request_wal_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_request_wal_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_wal_batch ON COMMIT DROP AS
    SELECT * FROM public.request_wal_default
    WHERE created_at < now() - p_retention
    ORDER BY created_at
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_wal_default
    WHERE request_id IN (SELECT request_id FROM _promote_wal_batch)
      AND created_at IN (SELECT created_at FROM _promote_wal_batch);
    
    BEGIN
        INSERT INTO public.request_wal
        SELECT * FROM _promote_wal_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_wal_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_wal_default_batch(interval, int) IS
'Move one batch of cold rows from request_wal_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 3. promote_usage_ledger_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_usage_ledger_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_ul_batch ON COMMIT DROP AS
    SELECT * FROM public.usage_ledger_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.usage_ledger_default
    WHERE id IN (SELECT id FROM _promote_ul_batch);
    
    BEGIN
        INSERT INTO public.usage_ledger
        SELECT * FROM _promote_ul_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_usage_ledger_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_usage_ledger_default_batch(interval, int) IS
'Move one batch of cold rows from usage_ledger_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 4. promote_routing_decision_log_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_routing_decision_log_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rdl_batch ON COMMIT DROP AS
    SELECT * FROM public.routing_decision_log_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.routing_decision_log_default
    WHERE ts IN (SELECT ts FROM _promote_rdl_batch)
      AND request_id IN (SELECT request_id FROM _promote_rdl_batch);
    
    BEGIN
        INSERT INTO public.routing_decision_log
        SELECT * FROM _promote_rdl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_routing_decision_log_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_routing_decision_log_default_batch(interval, int) IS
'Move one batch of cold rows from routing_decision_log_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 5. promote_credential_model_index_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_credential_model_index_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_cmi_batch ON COMMIT DROP AS
    SELECT * FROM public.credential_model_index_default
    WHERE bucket < now() - p_retention
    ORDER BY bucket
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.credential_model_index_default
    WHERE bucket IN (SELECT bucket FROM _promote_cmi_batch)
      AND credential_id IN (SELECT credential_id FROM _promote_cmi_batch)
      AND raw_model IN (SELECT raw_model FROM _promote_cmi_batch);
    
    BEGIN
        INSERT INTO public.credential_model_index
        SELECT * FROM _promote_cmi_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_credential_model_index_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_credential_model_index_default_batch(interval, int) IS
'Move one batch of cold rows from credential_model_index_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 6. promote_request_logs_bodies_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_request_logs_bodies_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_rlb_batch ON COMMIT DROP AS
    SELECT * FROM public.request_logs_bodies_default
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.request_logs_bodies_default
    WHERE request_id IN (SELECT request_id FROM _promote_rlb_batch)
      AND ts IN (SELECT ts FROM _promote_rlb_batch);
    
    BEGIN
        INSERT INTO public.request_logs_bodies
        SELECT * FROM _promote_rlb_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_request_logs_bodies_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_request_logs_bodies_default_batch(interval, int) IS
'Move one batch of cold rows from request_logs_bodies_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 7. promote_credit_ledger_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_credit_ledger_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_cl_batch ON COMMIT DROP AS
    SELECT * FROM public.credit_ledger_default
    WHERE created_at < now() - p_retention
    ORDER BY created_at
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.credit_ledger_default
    WHERE id IN (SELECT id FROM _promote_cl_batch);
    
    BEGIN
        INSERT INTO public.credit_ledger
        SELECT * FROM _promote_cl_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_credit_ledger_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_credit_ledger_default_batch(interval, int) IS
'Move one batch of cold rows from credit_ledger_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

-- ============================================================
-- 8. promote_tool_usage_stats_default_batch
-- ============================================================
CREATE OR REPLACE FUNCTION promote_tool_usage_stats_default_batch(
    p_retention interval DEFAULT '7 days',
    p_batch_size int     DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
BEGIN
    CREATE TEMP TABLE _promote_tus_batch ON COMMIT DROP AS
    SELECT * FROM public.tool_usage_stats_default
    WHERE usage_date < CURRENT_DATE - (p_retention::int / 86400)  -- 把 interval 转成天数
    ORDER BY usage_date
    LIMIT p_batch_size;
    
    GET DIAGNOSTICS n = ROW_COUNT;
    
    IF n = 0 THEN
        RETURN 0;
    END IF;
    
    DELETE FROM public.tool_usage_stats_default
    WHERE id IN (SELECT id FROM _promote_tus_batch)
      AND usage_date IN (SELECT usage_date FROM _promote_tus_batch);
    
    BEGIN
        INSERT INTO public.tool_usage_stats
        SELECT * FROM _promote_tus_batch
        ON CONFLICT DO NOTHING;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_tool_usage_stats_default_batch: INSERT failed (%), rows preserved in _default', SQLERRM;
        n := 0;
    END;
    
    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_tool_usage_stats_default_batch(interval, int) IS
'Move one batch of cold rows from tool_usage_stats_default into the matching monthly partition.
Fixed 2026-07-04 in migration 339.';

COMMIT;

-- ============================================================
-- 验证：所有 8 个函数应该已创建
-- ============================================================
DO $$
DECLARE
    func_count int;
BEGIN
    SELECT COUNT(*) INTO func_count
    FROM pg_proc
    WHERE proname IN (
        'promote_request_logs_default_batch',
        'promote_request_wal_default_batch',
        'promote_usage_ledger_default_batch',
        'promote_routing_decision_log_default_batch',
        'promote_credential_model_index_default_batch',
        'promote_request_logs_bodies_default_batch',
        'promote_credit_ledger_default_batch',
        'promote_tool_usage_stats_default_batch'
    );
    
    IF func_count <> 8 THEN
        RAISE EXCEPTION 'Expected 8 promote functions, found %', func_count;
    END IF;
    
    RAISE NOTICE 'Migration 339 complete: % promote functions installed', func_count;
END
$$;