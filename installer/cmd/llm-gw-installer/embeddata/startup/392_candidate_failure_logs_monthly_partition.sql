-- 392_candidate_failure_logs_monthly_partition.sql
-- 2026-07-13 P1: candidate_failure_logs 拆为 hot + 月度分区
--
-- 设计背景：
--   candidate_failure_logs 当前是 heap 堆表（无分区），每请求每失败
--   候选 1 行。在高 QPS 错误场景下增长极快，月增数 GB。
--   30d DROP 已在 391 迁移中加（drop_old_state_partitions）。
--   本迁移将表拆为 hot + 月度分区（参考 model_probe_runs 模式）：
--     - candidate_failure_logs_hot: heap，独立索引，24h 默认保留
--     - candidate_failure_logs: 月度列分区（columnar），30d 默认 DROP
--   业务查询通过 candidate_failure_logs_with_current_month 视图。
--
-- 重要：candidate_failure_logs 当前是 heap 无主键（id 不唯一）。
-- 沿用 model_probe_runs 模式：id 列不强制唯一，应用层去重。

BEGIN;

-- ═══════════════════════════════════════════════════════════════
-- 1. 创建独立热表 candidate_failure_logs_hot (HEAP)
-- ═══════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS candidate_failure_logs_hot (
    id                          bigint,
    request_id                  text NOT NULL,
    ts                          timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id                   text DEFAULT 'default' NOT NULL,
    credential_id               integer NOT NULL,
    provider_id                 integer NOT NULL,
    raw_model_name              text NOT NULL,
    attempt_index               integer DEFAULT 0 NOT NULL,
    error_kind                  text NOT NULL,
    error_message               text,
    upstream_status_code        integer,
    upstream_response_body      text,
    upstream_response_preview   text,
    latency_ms                  integer,
    retryable                   boolean,
    context                     jsonb
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created candidate_failure_logs_hot table'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 2. 创建索引（与原表一致）
-- ═══════════════════════════════════════════════════════════════

CREATE INDEX IF NOT EXISTS idx_cfl_hot_cred_ts
    ON candidate_failure_logs_hot (credential_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_cfl_hot_provider_ts
    ON candidate_failure_logs_hot (provider_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_cfl_hot_model_ts
    ON candidate_failure_logs_hot (raw_model_name, ts DESC);

CREATE INDEX IF NOT EXISTS idx_cfl_hot_request_id
    ON candidate_failure_logs_hot (request_id);

DO $$ BEGIN RAISE NOTICE 'Created indexes on candidate_failure_logs_hot'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 3. 将原表改为分区表（保留列存储不变量）
-- ═══════════════════════════════════════════════════════════════
--
-- 关键：原 candidate_failure_logs 在 phase-22 中被设为 columnar 堆表。
-- 改为分区表时保持 columnar（与 model_probe_runs 一致）。
-- 由于 id 不唯一，分区表不加 PRIMARY KEY。

-- 3.1 重命名原表（保留数据）
DO $$
DECLARE
    has_default boolean;
    has_partitions boolean;
BEGIN
    -- 检查原表是否已是分区表
    SELECT EXISTS (
        SELECT 1 FROM pg_partitioned_table
        WHERE partrelid = 'public.candidate_failure_logs'::regclass
    ) INTO has_partitions;

    IF NOT has_partitions THEN
        -- 重命名原表为 _old_columnar
        ALTER TABLE IF EXISTS public.candidate_failure_logs RENAME TO candidate_failure_logs_columnar_archive;

        -- 创建新的分区表
        EXECUTE $sql$
            CREATE TABLE public.candidate_failure_logs (
                id                          bigint,
                request_id                  text NOT NULL,
                ts                          timestamp with time zone DEFAULT now() NOT NULL,
                tenant_id                   text DEFAULT 'default'::text NOT NULL,
                credential_id               integer NOT NULL,
                provider_id                 integer NOT NULL,
                raw_model_name              text NOT NULL,
                attempt_index               integer DEFAULT 0 NOT NULL,
                error_kind                  text NOT NULL,
                error_message               text,
                upstream_status_code        integer,
                upstream_response_body      text,
                upstream_response_preview   text,
                latency_ms                  integer,
                retryable                   boolean,
                context                     jsonb
            ) PARTITION BY RANGE (ts)
        $sql$;

        RAISE NOTICE 'Converted candidate_failure_logs to partitioned table';
    ELSE
        RAISE NOTICE 'candidate_failure_logs is already partitioned; skipping conversion';
    END IF;
END $$;

DO $$ BEGIN RAISE NOTICE 'Candidate_failure_logs partition setup done'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 4. 创建月度分区函数
-- ═══════════════════════════════════════════════════════════════

CREATE OR REPLACE FUNCTION ensure_candidate_failure_logs_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'candidate_failure_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF candidate_failure_logs
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_candidate_failure_logs_partition: created % as columnar', partition_name;
    ELSE
        -- Idempotency: 确保现有分区保持 columnar
        PERFORM enforce_columnar_partition(partition_name, 'candidate_failure_logs');
    END IF;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION ensure_candidate_failure_logs_partition(timestamp with time zone) IS
'Ensure monthly partition for candidate_failure_logs (INSERT-only after promote).
Created USING columnar to preserve storage strategy and enable compression.
Added 2026-07-13 by Migration 392.';

-- 创建当月和下月分区
SELECT ensure_candidate_failure_logs_partition(date_trunc('month', NOW())::timestamp);
SELECT ensure_candidate_failure_logs_partition((date_trunc('month', NOW()) + interval '1 month')::timestamp);

DO $$ BEGIN RAISE NOTICE 'Ensured current and next month partitions for candidate_failure_logs'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 5. 恢复 RLS 策略
-- ═══════════════════════════════════════════════════════════════

ALTER TABLE candidate_failure_logs_hot ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot;
CREATE POLICY tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot
    USING (tenant_id = get_current_tenant());

DO $$ BEGIN RAISE NOTICE 'RLS policy applied to candidate_failure_logs_hot'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 6. 创建视图 candidate_failure_logs_with_current_month
-- ═══════════════════════════════════════════════════════════════

DROP VIEW IF EXISTS candidate_failure_logs_with_current_month;
CREATE VIEW candidate_failure_logs_with_current_month AS
SELECT * FROM candidate_failure_logs_hot
UNION ALL
SELECT * FROM candidate_failure_logs;

COMMENT ON VIEW candidate_failure_logs_with_current_month IS
'Optimized query VIEW using hot table architecture.
- candidate_failure_logs_hot: independent hot table (default 24h retention)
- candidate_failure_logs: parent table (auto-aggregates all ATTACHED monthly partitions, columnar storage)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 392 (2026-07-13).';

DO $$ BEGIN RAISE NOTICE 'Created candidate_failure_logs_with_current_month view'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 7. 创建 promote 函数（hot → 月度分区）
-- ═══════════════════════════════════════════════════════════════

CREATE OR REPLACE FUNCTION promote_candidate_failure_logs_hot_to_partition(
    p_retention interval DEFAULT '24 hours',
    p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
    month_rec RECORD;
BEGIN
    -- 确保目标分区存在
    FOR month_rec IN
        SELECT DISTINCT date_trunc('month', ts) AS m
        FROM candidate_failure_logs_hot
        WHERE ts < now() - p_retention
        LIMIT 12
    LOOP
        PERFORM ensure_candidate_failure_logs_partition(month_rec.m);
    END LOOP;

    -- 临时表批量复制
    EXECUTE 'DROP TABLE IF EXISTS pg_temp._promote_cfl_hot_batch';
    CREATE TEMP TABLE _promote_cfl_hot_batch ON COMMIT DROP AS
    SELECT * FROM candidate_failure_logs_hot
    WHERE ts < now() - p_retention
    ORDER BY ts
    LIMIT p_batch_size;

    GET DIAGNOSTICS n = ROW_COUNT;

    IF n = 0 THEN
        RETURN 0;
    END IF;

    -- 从 hot 表删除
    DELETE FROM candidate_failure_logs_hot
    WHERE ctid IN (
        SELECT ctid FROM _promote_cfl_hot_batch
    );

    -- 插入到父表（PG 自动路由到对应月度分区）
    BEGIN
        INSERT INTO candidate_failure_logs
        SELECT * FROM _promote_cfl_hot_batch;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_candidate_failure_logs_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
        n := 0;
    END;

    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_candidate_failure_logs_hot_to_partition(interval, int) IS
'Promotes cold rows from candidate_failure_logs_hot to monthly columnar partitions.
Created by migration 392 (2026-07-13).';

-- 添加到 partition_manager 的 promoteSpecs 注册（Go 侧在迁移后已自动识别）
-- 这里仅在 SQL 端暴露，Go 端需要在 promoteSpecs() 中追加：
--   {fnName: "promote_candidate_failure_logs_hot_to_partition", label: "candidate_failure_logs_hot"}

DO $$ BEGIN RAISE NOTICE 'Created promote_candidate_failure_logs_hot_to_partition'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 8. 验证迁移
-- ═══════════════════════════════════════════════════════════════
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'candidate_failure_logs_hot') THEN
        RAISE EXCEPTION 'candidate_failure_logs_hot was not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'candidate_failure_logs' AND relkind = 'p') THEN
        RAISE EXCEPTION 'candidate_failure_logs was not converted to partitioned table';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'promote_candidate_failure_logs_hot_to_partition') THEN
        RAISE EXCEPTION 'promote_candidate_failure_logs_hot_to_partition was not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'candidate_failure_logs_with_current_month') THEN
        RAISE EXCEPTION 'candidate_failure_logs_with_current_month view was not created';
    END IF;
    RAISE NOTICE '392_candidate_failure_logs_monthly_partition: all objects created successfully';
END $$;

COMMIT;

-- ═══════════════════════════════════════════════════════════════
-- 部署后人工验证步骤
-- ═══════════════════════════════════════════════════════════════
--
-- 1. 验证视图存在：
--    SELECT * FROM candidate_failure_logs_with_current_month LIMIT 1;
--
-- 2. 验证 promote 函数：
--    SELECT promote_candidate_failure_logs_hot_to_partition('24 hours'::interval, 1000);
--
-- 3. 验证分区表已分区：
--    SELECT p.relname AS parent, count(*) AS partitions
--    FROM pg_inherits i JOIN pg_class p ON p.oid = i.inhparent
--    WHERE p.relname = 'candidate_failure_logs' GROUP BY 1;
--
-- 4. 监控 INSERT 是否走 hot：
--    EXPLAIN INSERT INTO candidate_failure_logs_hot VALUES (...);
