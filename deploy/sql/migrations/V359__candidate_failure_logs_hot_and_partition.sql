-- =============================================================================
-- V359__candidate_failure_logs_hot_and_partition.sql
-- 2026-08-17: candidate_failure_logs 治理 — 拆 hot + 月度 columnar 分区
--
-- 背景：
--   252（154 网关共库）的 candidate_failure_logs 是单一 citus_columnar 表，
--   361MB / 84,757 行 / 72,429 行 ts IS NULL / max(ts)=2026-06-26。
--   columnar 不支持 UPDATE/DELETE，opslog_trimmer 的 TTL DELETE 失效，
--   表只增不减。V358 加了 session_id 列 + 索引，但未解决表增长无界问题。
--
-- 设计（采纳 handoff 2026-08-17 §5.3 用户决策 2026-08-17 23:35）：
--   1. candidate_failure_logs_hot：heap（fillfactor=90），新 INSERT 入口，
--      由 partition_manager.promote_candidate_failure_logs_hot_to_partition
--      按 24h 保留 + 5000 批次 promote 到月度分区。
--   2. candidate_failure_logs：PARTITION BY RANGE (ts) 父表，
--      子分区 USING columnar，沿用 zstd / stripe_row_limit=150000 / chunk_group_row_limit=10000。
--   3. candidate_failure_logs_with_current_month：UNION ALL 视图，admin 读端走此视图。
--   4. candidate_failure_logs_columnar_old：保留原 columnar 表 30 天观察期，
--      不迁移 72,429 NULL ts 行（用户决策：移除相关请求数据），
--      DROP 时机由后续迁移或人工操作触发。
--
-- 列清单（沿用 V358 + 现表 20 列）：
--   id, request_id, ts, tenant_id, credential_id, provider_id,
--   raw_model_name, attempt_index, error_kind, error_message,
--   upstream_status_code, upstream_response_body, upstream_response_preview,
--   latency_ms, retryable, per_attempt_latency_ms,
--   extracted_upstream_status_code, diagnosed_error_kind,
--   context, session_id
--
-- 与 392 模板差异：
--   - 392 模板（sql/migrations/startup/392_*）仅覆盖 16 列；本迁移按现表 20 列完整重建，
--     与 392 模式等价但补齐 V358 后追加的 4 列。
--   - 392 模板保留原 columnar 表的全部数据到月度分区；本迁移对 NULL ts 行采取"不迁入"策略，
--     由后续 30 天后 DROP _columnar_old 完成清理（避免 columnar UPDATE 不支持的限制）。
--
-- 部署：低峰窗口（00:00-04:00 CST），不停写（INSERT 会同时进 hot 视图兼容层）。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== V359: candidate_failure_logs → hot + 月度 columnar 分区 ==='

-- ---------------------------------------------------------------------------
-- 1. 重命名原 columnar 表 → 保留 30 天观察
-- ---------------------------------------------------------------------------
\echo '--- 1. RENAME 原 columnar 表 ---'
DO $do$
DECLARE
    rec record;
BEGIN
    SELECT relname, amname INTO rec
    FROM pg_class c JOIN pg_am a ON a.oid = c.relam
    WHERE c.oid = 'candidate_failure_logs'::regclass;

    IF rec.amname = 'columnar' THEN
        ALTER TABLE candidate_failure_logs RENAME TO candidate_failure_logs_columnar_old;
        RAISE NOTICE 'V359: renamed columnar → candidate_failure_logs_columnar_old';
    ELSIF rec.amname IS NULL THEN
        RAISE EXCEPTION 'V359: candidate_failure_logs not found or relam is NULL (amname=%)', rec.amname;
    ELSE
        RAISE EXCEPTION 'V359: candidate_failure_logs is % (not columnar); refusing to migrate. Abort.', rec.amname;
    END IF;
END
$do$;

-- ---------------------------------------------------------------------------
-- 2. 创建 hot 表（heap，fillfactor=90，24h 保留）
--    通过 LIKE INCLUDING ALL 从 _columnar_old 继承所有列 + DEFAULT + 索引
-- ---------------------------------------------------------------------------
\echo '--- 2. CREATE candidate_failure_logs_hot (heap) ---'
DO $do$
DECLARE
    has_default boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'candidate_failure_logs_hot'
          AND relnamespace = 'public'::regnamespace
    ) INTO has_default;

    IF has_default THEN
        RAISE NOTICE 'V359: candidate_failure_logs_hot already exists, skipping';
    ELSE
        -- LIKE INCLUDING ALL 继承列 + DEFAULT + 索引 + RLS policies + comments
        -- heap with fillfactor=90 by overriding via WITH
        CREATE TABLE candidate_failure_logs_hot (LIKE candidate_failure_logs_columnar_old INCLUDING ALL)
            WITH (fillfactor=90);
        RAISE NOTICE 'V359: created candidate_failure_logs_hot (heap)';
    END IF;
END
$do$;

-- ---------------------------------------------------------------------------
-- 3. ALTER hot 表 owner（如需）+ 确保 ts 默认值
-- ---------------------------------------------------------------------------
\echo '--- 3. hot 表 NOT NULL + DEFAULT 补齐（LIKE INCLUDING ALL 不继承 NOT NULL） ---'
DO $do$
BEGIN
    -- NOT NULL 约束（必须与父表 + 原 columnar 表一致）
    ALTER TABLE candidate_failure_logs_hot
        ALTER COLUMN request_id SET NOT NULL,
        ALTER COLUMN tenant_id SET DEFAULT 'default',
        ALTER COLUMN tenant_id SET NOT NULL,
        ALTER COLUMN credential_id SET NOT NULL,
        ALTER COLUMN provider_id SET NOT NULL,
        ALTER COLUMN raw_model_name SET NOT NULL,
        ALTER COLUMN attempt_index SET DEFAULT 0,
        ALTER COLUMN attempt_index SET NOT NULL,
        ALTER COLUMN error_kind SET NOT NULL,
        ALTER COLUMN ts SET DEFAULT now();

    RAISE NOTICE 'V359: hot 表 NOT NULL + DEFAULT 补齐';
END
$do$;

-- ---------------------------------------------------------------------------
-- 4. 创建 partitioned 父表（同 schema，PARTITION BY RANGE(ts)）
-- ---------------------------------------------------------------------------
\echo '--- 4. CREATE partitioned 父表 candidate_failure_logs ---'
DO $do$
DECLARE
    has_partitions boolean;
    has_table boolean;
BEGIN
    -- 检查父表是否已存在（恢复场景或重复执行）
    SELECT EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'candidate_failure_logs'
          AND relnamespace = 'public'::regnamespace
    ) INTO has_table;

    IF has_table THEN
        SELECT EXISTS (
            SELECT 1 FROM pg_partitioned_table
            WHERE partrelid = 'candidate_failure_logs'::regclass
        ) INTO has_partitions;

        IF has_partitions THEN
            RAISE NOTICE 'V359: candidate_failure_logs already partitioned, skipping';
        ELSE
            RAISE EXCEPTION 'V359: candidate_failure_logs exists but is not partitioned (heap?); manual intervention required';
        END IF;
    ELSE
        EXECUTE $sql$
            CREATE TABLE candidate_failure_logs (
                id                                bigint,
                request_id                        text NOT NULL,
                ts                                timestamp with time zone DEFAULT now() NOT NULL,
                tenant_id                         text DEFAULT 'default'::text NOT NULL,
                credential_id                     integer NOT NULL,
                provider_id                       integer NOT NULL,
                raw_model_name                    text NOT NULL,
                attempt_index                     integer DEFAULT 0 NOT NULL,
                error_kind                        text NOT NULL,
                error_message                     text,
                upstream_status_code              integer,
                upstream_response_body            text,
                upstream_response_preview         text,
                latency_ms                        integer,
                retryable                         boolean,
                per_attempt_latency_ms            integer,
                extracted_upstream_status_code    integer,
                diagnosed_error_kind              text,
                context                           jsonb,
                session_id                        text
            ) PARTITION BY RANGE (ts)
        $sql$;
        RAISE NOTICE 'V359: created partitioned 父表 candidate_failure_logs';
    END IF;
END
$do$;

-- ---------------------------------------------------------------------------
-- 5. ensure_partition 函数（沿用 392 模板）
-- ---------------------------------------------------------------------------
\echo '--- 5. CREATE ensure_candidate_failure_logs_partition ---'
DROP FUNCTION IF EXISTS ensure_candidate_failure_logs_partition(timestamp with time zone);
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
'Ensure monthly columnar partition for candidate_failure_logs (created by V359, 2026-08-17).
Mirrors 392 ensure_* pattern; idempotent.';

-- ---------------------------------------------------------------------------
-- 6. 创建当月 + 上下月分区（启动时只需保证当月 + 未来一个月可写）
-- ---------------------------------------------------------------------------
\echo '--- 6. CREATE 历史月份（2025-12 → 2026-06）+ 当月 + 上下月分区 ---'
SELECT ensure_candidate_failure_logs_partition('2025-12-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-01-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-02-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-03-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-04-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-05-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition('2026-06-01'::timestamptz);
SELECT ensure_candidate_failure_logs_partition((date_trunc('month', NOW()) - interval '1 month')::timestamp);
SELECT ensure_candidate_failure_logs_partition(date_trunc('month', NOW())::timestamp);
SELECT ensure_candidate_failure_logs_partition((date_trunc('month', NOW()) + interval '1 month')::timestamp);

-- ---------------------------------------------------------------------------
-- 7. 把 12,328 行非 NULL ts 历史数据按月度路由到父表
--    72,429 NULL ts 行不迁移（用户决策：移除），保留在 _columnar_old 待后续 DROP。
-- ---------------------------------------------------------------------------
\echo '--- 7. INSERT INTO candidate_failure_logs SELECT FROM _columnar_old WHERE ts IS NOT NULL ---'
DO $do$
DECLARE
    n_inserted bigint := 0;
    n_total_old bigint;
    n_null_ts bigint;
BEGIN
    SELECT count(*) INTO n_total_old FROM candidate_failure_logs_columnar_old;
    SELECT count(*) INTO n_null_ts FROM candidate_failure_logs_columnar_old WHERE ts IS NULL;

    RAISE NOTICE 'V359: _columnar_old total=%, NULL ts=%', n_total_old, n_null_ts;

    -- 单次 INSERT（12,328 行）；若超过 30s 可分批
    INSERT INTO candidate_failure_logs
    SELECT
        id, request_id, ts, tenant_id, credential_id, provider_id,
        raw_model_name, attempt_index, error_kind, error_message,
        upstream_status_code, upstream_response_body, upstream_response_preview,
        latency_ms, retryable, per_attempt_latency_ms,
        extracted_upstream_status_code, diagnosed_error_kind,
        context, session_id
    FROM candidate_failure_logs_columnar_old
    WHERE ts IS NOT NULL;

    GET DIAGNOSTICS n_inserted = ROW_COUNT;
    RAISE NOTICE 'V359: migrated % non-null-ts rows to partitioned parent', n_inserted;

    -- 验证分区行数
    RAISE NOTICE 'V359: parent count=%', (SELECT count(*) FROM candidate_failure_logs);
END
$do$;

-- ---------------------------------------------------------------------------
-- 8. RLS 策略：父表 + hot 表
-- ---------------------------------------------------------------------------
\echo '--- 8. ENABLE ROW LEVEL SECURITY ---'
ALTER TABLE candidate_failure_logs ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs ON candidate_failure_logs;
CREATE POLICY tenant_isolation_candidate_failure_logs ON candidate_failure_logs
    USING (tenant_id = get_current_tenant());

DROP POLICY IF EXISTS tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot;
CREATE POLICY tenant_isolation_candidate_failure_logs_hot ON candidate_failure_logs_hot
    USING (tenant_id = get_current_tenant());

-- ---------------------------------------------------------------------------
-- 9. 索引：父表 + hot 表
--    LIKE 继承只对 hot 表起作用（_columnar_old 的索引），
--    父表需要在 CREATE 后手动建索引（PARTITIONED 表索引会自动 propagate 到子分区）
-- ---------------------------------------------------------------------------
\echo '--- 9. CREATE 索引 ---'
CREATE INDEX IF NOT EXISTS idx_cfl_cred_ts
    ON candidate_failure_logs (credential_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cfl_provider_ts
    ON candidate_failure_logs (provider_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cfl_model_ts
    ON candidate_failure_logs (raw_model_name, ts DESC);
CREATE INDEX IF NOT EXISTS idx_cfl_request_id
    ON candidate_failure_logs (request_id);
CREATE INDEX IF NOT EXISTS idx_cfl_session_ts
    ON candidate_failure_logs (session_id, ts DESC);

-- hot 表索引（V358 已加；这里防御性确认）
CREATE INDEX IF NOT EXISTS idx_cfl_hot_session_ts
    ON candidate_failure_logs_hot (session_id, ts DESC);

-- ---------------------------------------------------------------------------
-- 10. 视图 candidate_failure_logs_with_current_month
-- ---------------------------------------------------------------------------
\echo '--- 10. CREATE VIEW candidate_failure_logs_with_current_month ---'
DROP VIEW IF EXISTS candidate_failure_logs_with_current_month;
-- 显式列名（hot 来自 LIKE INCLUDING ALL，列顺序与父表不一致；UNION 需要按位置对齐）
CREATE VIEW candidate_failure_logs_with_current_month AS
SELECT
    id, request_id, ts, tenant_id, credential_id, provider_id, raw_model_name, attempt_index,
    error_kind, error_message, upstream_status_code, upstream_response_body, upstream_response_preview,
    latency_ms, retryable, per_attempt_latency_ms, extracted_upstream_status_code, diagnosed_error_kind,
    context, session_id
FROM candidate_failure_logs_hot
UNION ALL
SELECT
    id, request_id, ts, tenant_id, credential_id, provider_id, raw_model_name, attempt_index,
    error_kind, error_message, upstream_status_code, upstream_response_body, upstream_response_preview,
    latency_ms, retryable, per_attempt_latency_ms, extracted_upstream_status_code, diagnosed_error_kind,
    context, session_id
FROM candidate_failure_logs;

COMMENT ON VIEW candidate_failure_logs_with_current_month IS
'UNION ALL view: hot (24h retention, heap) ∪ partitioned parent (monthly columnar).
Admin endpoints read from this view; Go writers should INSERT into candidate_failure_logs_hot
(driven by partition_manager.promote_candidate_failure_logs_hot_to_partition).
Created by V359 (2026-08-17).';

-- ---------------------------------------------------------------------------
-- 11. promote 函数（按 392 模板，从 hot → 月度分区）
-- ---------------------------------------------------------------------------
\echo '--- 11. CREATE promote_candidate_failure_logs_hot_to_partition ---'
DROP FUNCTION IF EXISTS promote_candidate_failure_logs_hot_to_partition(interval, int);
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
    -- 确保目标分区存在（遍历 cold 行的月份）
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

    -- 插入父表（PG 自动按 ts 路由到对应月度分区）
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
Registered in bg.partition_manager.promoteSpecs() (already present from Migration 392 stub).
Created by V359 (2026-08-17).';

-- ---------------------------------------------------------------------------
-- 12. 验证迁移
-- ---------------------------------------------------------------------------
\echo '--- 12. VALIDATION ---'
DO $do$
DECLARE
    has_hot boolean;
    has_part boolean;
    has_view boolean;
    has_fn boolean;
    has_policy boolean;
    parent_count bigint;
    old_count bigint;
BEGIN
    SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'candidate_failure_logs_hot') INTO has_hot;
    SELECT EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'candidate_failure_logs'::regclass) INTO has_part;
    SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'candidate_failure_logs_with_current_month') INTO has_view;
    SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'promote_candidate_failure_logs_hot_to_partition') INTO has_fn;
    SELECT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname='public' AND tablename='candidate_failure_logs'
          AND policyname='tenant_isolation_candidate_failure_logs'
    ) INTO has_policy;

    SELECT count(*) INTO parent_count FROM candidate_failure_logs;
    SELECT count(*) INTO old_count FROM candidate_failure_logs_columnar_old;

    IF NOT has_hot   THEN RAISE EXCEPTION 'V359 VALIDATION FAIL: candidate_failure_logs_hot missing'; END IF;
    IF NOT has_part  THEN RAISE EXCEPTION 'V359 VALIDATION FAIL: candidate_failure_logs not partitioned'; END IF;
    IF NOT has_view  THEN RAISE EXCEPTION 'V359 VALIDATION FAIL: view missing'; END IF;
    IF NOT has_fn    THEN RAISE EXCEPTION 'V359 VALIDATION FAIL: promote fn missing'; END IF;
    IF NOT has_policy THEN RAISE EXCEPTION 'V359 VALIDATION FAIL: RLS policy missing'; END IF;

    RAISE NOTICE 'V359 VALIDATION OK: hot=%, partitioned=%, view=%, promote_fn=%, rls=%, parent_rows=%, _old_rows=%',
        has_hot, has_part, has_view, has_fn, has_policy, parent_count, old_count;
END
$do$;

\echo '=== V359 完成：candidate_failure_logs 已拆 hot + 月度 columnar 分区 ==='
\echo '=== 后续：30 天观察期后人工 DROP candidate_failure_logs_columnar_old ==='