-- Migration 386: model_probe_runs 热表独立化 + 分区架构
--
-- 背景：
--   model_probe_runs 是记录每次模型探测结果的审计表。252 上当前有：
--   - 243 MB / 595,086 行（截至 2026-06-26）
--   - 写入频率 ~74k 行/天
--   - 索引: (credential_id, created_at DESC), (raw_model_name, created_at DESC),
--           (status, created_at DESC) WHERE status <> 'ok'
--   - Access method: columnar（不可 UPDATE/DELETE）
--   - RLS: tenant_isolation_model_probe_runs
--
-- 问题：
--   - columnar 表无法 DELETE 单行（不支持 CTID 扫描）
--   - 数据保留策略不清晰（只能 TRUNCATE 整表，丢失所有历史）
--   - 当前数据增长快但缺少生命周期管理
--
-- 解决方案：统一为 hot + 月度分区架构（参考 migration 341/346）：
--   1. 新增 model_probe_runs_hot (独立 HEAP 表)，写入路径改到这里
--   2. 原始 model_probe_runs (columnar) 改造为 PARTITIONED BY RANGE(created_at)
--      - 历史分区: COLUMNAR（保留原访问方法，压缩）
--      - 注意: 由于 columnar 不支持 UPDATE/DELETE，promote 操作只走 hot→父表路径
--   3. 删除 DEFAULT 分区（避免命中 columnar 默认分区导致写入失败）
--   4. 创建 model_probe_runs_with_current_month VIEW（hot + 历史分区 UNION）
--   5. 创建 promote_model_probe_runs_hot_to_partition() 函数
--
-- 数据保留策略（可通过 settings 配置）：
--   - Hot 表: 默认保留 24 小时（settings: probe.hot_retention_hours）
--   - 历史分区: 默认保留 90 天（settings: probe.partition_retention_days）
--   - 由 bg.PartitionManager 周期性 promote + drop
--
-- 数据迁移：
--   本地迁移策略：只迁移结构，不迁移数据（用户决策，2026-07-11）。
--   252 上的 595k 行历史数据将保留在 model_probe_runs_old 中。
--   新写入从 0 开始（hot 表为空），由 probe runner 重新生成。
--
-- 参考：
--   - migration 341 (request_logs 热表独立化模板)
--   - migration 346 (routing_decision_log 热表独立化)
--   - phase-23-columnar-invariant (columnar 分区规范)
--
-- Author: llm-gateway-ops (2026-07-11)

BEGIN;

-- ============================================================
-- 0. 备份原表数据到 model_probe_runs_old
-- ============================================================

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'model_probe_runs' AND relkind = 'r')
       AND NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'model_probe_runs_old')
    THEN
        ALTER TABLE public.model_probe_runs RENAME TO model_probe_runs_old;
        RAISE NOTICE 'Renamed model_probe_runs to model_probe_runs_old';
    ELSIF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'model_probe_runs_old') THEN
        RAISE NOTICE 'model_probe_runs_old already exists, skipping rename';
    END IF;
END $$;

-- ============================================================
-- 1. 创建独立热表 model_probe_runs_hot (HEAP)
-- ============================================================

CREATE TABLE IF NOT EXISTS model_probe_runs_hot (
    id              bigint,
    tenant_id       text,
    credential_id   bigint,
    raw_model_name  text,
    status          text,
    http_status     integer,
    error_code      text,
    error_message   text,
    latency_ms      integer,
    state_change    text,
    state_applied   boolean,
    triggered_by    text,
    created_at      timestamp with time zone DEFAULT now()
) WITH (fillfactor=90);

DO $$ BEGIN RAISE NOTICE 'Created model_probe_runs_hot table'; END $$;

-- ============================================================
-- 2. 创建索引（与原表一致）
-- ============================================================

CREATE INDEX IF NOT EXISTS idx_mpr_hot_cred_created
    ON model_probe_runs_hot (credential_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mpr_hot_model_created
    ON model_probe_runs_hot (raw_model_name, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mpr_hot_status_created
    ON model_probe_runs_hot (status, created_at DESC)
    WHERE status <> 'ok';

CREATE INDEX IF NOT EXISTS idx_mpr_hot_created_at
    ON model_probe_runs_hot (created_at DESC);

CREATE INDEX IF NOT EXISTS idx_mpr_hot_tenant_created
    ON model_probe_runs_hot (tenant_id, created_at DESC);

DO $$ BEGIN RAISE NOTICE 'Created indexes on model_probe_runs_hot'; END $$;

-- ============================================================
-- 3. 创建新的 model_probe_runs 分区表（PARTITIONED BY RANGE created_at）
-- ============================================================

-- 注意：原表是 columnar 不支持 UPDATE/DELETE，
-- 新的分区表保持 columnar 不变量（历史分区 COLUMNAR）。
-- 由于 id 不唯一（应用层可能计算重复 id），分区表不加 PK。

CREATE TABLE IF NOT EXISTS model_probe_runs (
    id              bigint,
    tenant_id       text,
    credential_id   bigint,
    raw_model_name  text,
    status          text,
    http_status     integer,
    error_code      text,
    error_message   text,
    latency_ms      integer,
    state_change    text,
    state_applied   boolean,
    triggered_by    text,
    created_at      timestamp with time zone DEFAULT now()
) PARTITION BY RANGE (created_at);

DO $$ BEGIN RAISE NOTICE 'Created model_probe_runs partitioned table'; END $$;

-- ============================================================
-- 4. 确保当月和下月分区存在
-- ============================================================

CREATE OR REPLACE FUNCTION ensure_model_probe_runs_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'model_probe_runs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 历史分区使用 COLUMNAR（与原表一致），压缩节省空间
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF model_probe_runs
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_model_probe_runs_partition: created % as columnar', partition_name;
    ELSE
        -- Idempotency: 确保现有分区保持 columnar
        PERFORM enforce_columnar_partition(partition_name, 'model_probe_runs');
    END IF;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION ensure_model_probe_runs_partition(timestamp with time zone) IS
'Ensure monthly partition for model_probe_runs (INSERT-only after promote).
Created USING columnar to preserve original storage strategy and enable compression.
Phase 23 columnar invariant applies.
Added 2026-07-11 by Migration 385.';

-- 创建当月和下月分区
SELECT ensure_model_probe_runs_partition(date_trunc('month', NOW())::timestamp);
SELECT ensure_model_probe_runs_partition((date_trunc('month', NOW()) + interval '1 month')::timestamp);

DO $$ BEGIN RAISE NOTICE 'Ensured current and next month partitions'; END $$;

-- ============================================================
-- 5. 恢复 RLS 策略
-- ============================================================

ALTER TABLE model_probe_runs_hot ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_model_probe_runs ON model_probe_runs_hot;
CREATE POLICY tenant_isolation_model_probe_runs ON model_probe_runs_hot
    USING (tenant_id = get_current_tenant());

DO $$ BEGIN RAISE NOTICE 'RLS policy applied to model_probe_runs_hot'; END $$;

-- ============================================================
-- 6. 创建视图 model_probe_runs_with_current_month
-- ============================================================

DROP VIEW IF EXISTS model_probe_runs_with_current_month;

CREATE VIEW model_probe_runs_with_current_month AS
SELECT * FROM model_probe_runs_hot
UNION ALL
SELECT * FROM model_probe_runs;

COMMENT ON VIEW model_probe_runs_with_current_month IS
'Optimized query VIEW using hot table architecture.
- model_probe_runs_hot: independent hot table (default 24h retention, configurable)
- model_probe_runs: parent table (auto-aggregates all ATTACHED monthly partitions, columnar storage)
PostgreSQL partition pruning applies to parent table queries.
Created by migration 386 (2026-07-11).';

-- ============================================================
-- 7. 创建 promote 函数（hot → 月度分区）
-- ============================================================

CREATE OR REPLACE FUNCTION promote_model_probe_runs_hot_to_partition(
    p_retention interval DEFAULT '24 hours',
    p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    n bigint := 0;
    target_month timestamp;
BEGIN
    -- 确保目标分区存在（hot 数据可能跨越多个月份分区）
    -- 扫描所有待迁移行的 created_at 月份，逐个 ensure
    DECLARE
        month_rec RECORD;
    BEGIN
        FOR month_rec IN
            SELECT DISTINCT date_trunc('month', created_at) AS m
            FROM model_probe_runs_hot
            WHERE created_at < now() - p_retention
            LIMIT 12
        LOOP
            PERFORM ensure_model_probe_runs_partition(month_rec.m);
        END LOOP;
    END;

    -- 使用 ON COMMIT DROP 是事务级，同一事务多次调用会冲突
    -- 改用显式 DROP IF EXISTS（pg_temp.<name> 引用临时表）
    EXECUTE 'DROP TABLE IF EXISTS pg_temp._promote_mpr_hot_batch';
    CREATE TEMP TABLE _promote_mpr_hot_batch ON COMMIT DROP AS
    SELECT * FROM model_probe_runs_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at
    LIMIT p_batch_size;

    GET DIAGNOSTICS n = ROW_COUNT;

    IF n = 0 THEN
        RETURN 0;
    END IF;

    -- 从 hot 表删除这些行
    DELETE FROM model_probe_runs_hot
    WHERE ctid IN (
        SELECT ctid FROM _promote_mpr_hot_batch
    );

    -- 插入到父表（PG 自动路由到对应月度分区）
    BEGIN
        INSERT INTO model_probe_runs
        SELECT * FROM _promote_mpr_hot_batch;
    EXCEPTION WHEN OTHERS THEN
        RAISE WARNING 'promote_model_probe_runs_hot_to_partition: INSERT failed (%), rows preserved in hot table', SQLERRM;
        -- 回滚 DELETE（DELETE 已经发生，但 INSERT 失败，需要恢复 hot 表数据）
        INSERT INTO model_probe_runs_hot
        SELECT * FROM _promote_mpr_hot_batch;
        n := 0;
    END;

    RETURN n;
END;
$$;

COMMENT ON FUNCTION promote_model_probe_runs_hot_to_partition(interval, int) IS
'Move cold rows (older than p_retention, default 24h) from model_probe_runs_hot
to monthly partitions. Data is inserted into parent table and PostgreSQL
automatically routes to correct partition. Returns number of rows moved.
Loop until 0 to drain all cold data.
Created by migration 386 (2026-07-11).';

-- ============================================================
-- 8. 创建 drop_old_partitions 函数（保留 N 天内的分区）
-- ============================================================

CREATE OR REPLACE FUNCTION drop_old_model_probe_runs_partitions(
    p_retention_days int DEFAULT 90
)
RETURNS TABLE(dropped_partition text, rows_dropped bigint)
LANGUAGE plpgsql AS $$
DECLARE
    cutoff_date date := CURRENT_DATE - p_retention_days;
    rec RECORD;
BEGIN
    FOR rec IN
        SELECT
            c.relname AS partition_name,
            pg_total_relation_size(c.oid) AS size_bytes
        FROM pg_class c
        JOIN pg_inherits i ON i.inhrelid = c.oid
        JOIN pg_class p ON p.oid = i.inhparent
        WHERE p.relname = 'model_probe_runs'
          AND c.relname ~ '^model_probe_runs_[0-9]{4}_[0-9]{2}$'
    LOOP
        -- 从 partition name 解析月份 (例如 model_probe_runs_2026_05)
        DECLARE
            partition_month date := to_date(
                substring(rec.partition_name FROM 'model_probe_runs_([0-9]{4}_[0-9]{2})$'),
                'YYYY_MM'
            );
            month_end date := partition_month + interval '1 month';
        BEGIN
            IF month_end <= cutoff_date THEN
                EXECUTE format('DROP TABLE %I', rec.partition_name);
                RAISE NOTICE 'drop_old_model_probe_runs_partitions: dropped %', rec.partition_name;
                dropped_partition := rec.partition_name;
                rows_dropped := -1;  -- columnar 无法精确统计
                RETURN NEXT;
            END IF;
        END;
    END LOOP;
END;
$$;

COMMENT ON FUNCTION drop_old_model_probe_runs_partitions(int) IS
'Drop monthly partitions of model_probe_runs older than p_retention_days (default 90).
Used by bg.PartitionManager to enforce data retention policy.
Returns list of dropped partitions. Created by migration 386 (2026-07-11).';

-- ============================================================
-- 9. 验证
-- ============================================================

DO $$
DECLARE
    hot_count bigint;
    parent_exists boolean;
    promote_fn_exists boolean;
    view_exists boolean;
    rls_enabled boolean;
    partition_count int;
BEGIN
    -- 检查热表
    SELECT count(*) INTO hot_count FROM model_probe_runs_hot;
    RAISE NOTICE 'model_probe_runs_hot contains % rows', hot_count;

    -- 检查父表
    SELECT EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'model_probe_runs'
          AND relkind = 'p'
    ) INTO parent_exists;

    IF NOT parent_exists THEN
        RAISE EXCEPTION 'Partitioned table model_probe_runs not found';
    END IF;

    -- 检查分区数量
    SELECT count(*) INTO partition_count
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    JOIN pg_class p ON p.oid = i.inhparent
    WHERE p.relname = 'model_probe_runs';
    RAISE NOTICE 'model_probe_runs has % monthly partitions', partition_count;

    -- 检查 promote 函数
    SELECT EXISTS (
        SELECT 1 FROM pg_proc WHERE proname = 'promote_model_probe_runs_hot_to_partition'
    ) INTO promote_fn_exists;

    IF NOT promote_fn_exists THEN
        RAISE EXCEPTION 'Function promote_model_probe_runs_hot_to_partition not found';
    END IF;

    -- 检查视图
    SELECT EXISTS (
        SELECT 1 FROM pg_views WHERE viewname = 'model_probe_runs_with_current_month'
    ) INTO view_exists;

    IF NOT view_exists THEN
        RAISE EXCEPTION 'View model_probe_runs_with_current_month not found';
    END IF;

    -- 检查 RLS
    SELECT relrowsecurity INTO rls_enabled
    FROM pg_class WHERE relname = 'model_probe_runs_hot';

    IF NOT rls_enabled THEN
        RAISE EXCEPTION 'RLS not enabled on model_probe_runs_hot';
    END IF;

    RAISE NOTICE 'Migration 385 verification PASSED';
    RAISE NOTICE '  - hot table: % rows (HEAP)', hot_count;
    RAISE NOTICE '  - parent table: partitioned (% partitions)', partition_count;
    RAISE NOTICE '  - view: model_probe_runs_with_current_month (2-way UNION)';
    RAISE NOTICE '  - promote function: promote_model_probe_runs_hot_to_partition()';
    RAISE NOTICE '  - RLS: enabled';
END $$;

COMMIT;

-- ============================================================
-- 使用说明
-- ============================================================
--
-- 应用后：
--   1. 所有 INSERT 写入 model_probe_runs_hot
--   2. >24h 数据自动 promote 到月度分区（默认）
--   3. 跨月查询使用 model_probe_runs_with_current_month 视图
--   4. retention 配置: probe.hot_retention_hours (默认 24) / probe.partition_retention_days (默认 90)
--
-- 代码修改：
--   - INSERT INTO model_probe_runs → INSERT INTO model_probe_runs_hot
--   - SELECT ... FROM model_probe_runs WHERE created_at >= ... → model_probe_runs_hot (or use view)
--
-- 手动 promote 测试：
--   SELECT promote_model_probe_runs_hot_to_partition('24 hours', 100);
--
-- 手动 drop 旧分区：
--   SELECT * FROM drop_old_model_probe_runs_partitions(90);
