-- ===========================================================================
-- File:          sql/migrations/startup/616_provider_error_details_unique_constraint.sql
-- Database:      llm_gateway
-- Purpose:       为 provider_error_details 添加唯一约束以支持 UPSERT 聚合
--
-- Related:       bg/provider_error_aggregator.go
--                sql/migrations/startup/435_provider_quality_tables.sql
--                sql/migrations/startup/620_provider_error_details_tenant_scope.sql
-- Status:        active
-- Idempotent:    YES
--
-- Context:
--   2026-08-29 审计发现 provider_error_details 表缺少业务写入逻辑。
--   添加唯一约束以支持从 candidate_failure_logs_hot 的 UPSERT 聚合。
--
-- Fingerprint:
--   错误指纹 = (provider_id, model_name, endpoint, error_type, error_code, error_message)
--   相同指纹的错误会合并，occurrences 递增
--
-- 兼容性说明：
--   当 aggregation_bucket 列已经存在（例如 DB 从 252 同步并且已经包含更新
--   后的 schema），全局 fingerprint 不再与生产数据兼容：同一 provider 同一
--   fingerprint 可能分布在多个 10 分钟桶中，每个桶都是独立的合法聚合行。
--   此时 620 会在其后用 tenant-scoped 的 fingerprint 重建唯一索引，因此
--   616 需要让出。
--
--   当 aggregation_bucket 列不存在（正常首次部署顺序）时，全局 fingerprint
--   仍然有效；但如果表里已经写入了按 fingerprint 重复的行（例如聚合逻辑上
--   线前累积的历史），需要先按 fingerprint 聚合 occurrences / first_seen /
--   last_seen 再建索引，使旧数据与新逻辑一致。
--
-- Changelog:
--   2026-08-29  v1.0  初始创建 - 添加唯一约束
--   2026-09-03  v1.1  适配 252 同步场景：当 aggregation_bucket 已存在时跳过
--                     全局索引创建（620 会创建正确的 tenant-scoped 索引）；
--                     对历史重复行做发生次数合并后再建索引。
-- ===========================================================================

\set ON_ERROR_STOP on

DO $$
DECLARE
    has_bucket_col boolean;
    dup_groups int;
BEGIN
    -- Skip cleanly when migration 620's schema is already in place: the
    -- legacy global fingerprint would conflict with per-bucket rows that the
    -- newer aggregator has already written. 620 creates the correct
    -- tenant-scoped unique index, so 616 must not stand in the way.
    SELECT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name   = 'provider_error_details'
          AND column_name  = 'aggregation_bucket'
    ) INTO has_bucket_col;

    IF has_bucket_col THEN
        RAISE NOTICE 'migration 616: aggregation_bucket column already exists; deferring global fingerprint index to migration 620 (tenant-scoped)';
        RETURN;
    END IF;

    -- Aggregate pre-existing duplicates so the global fingerprint becomes
    -- valid. The aggregator contract groups rows by (provider_id,
    -- COALESCE(model_name,''), COALESCE(endpoint,''), error_type,
    -- COALESCE(error_code,''), LEFT(error_message,200)) and merges
    -- occurrences / first_seen_at / last_seen_at.
    --
    -- Implementation: pick the survivor (latest last_seen_at, then highest id)
    -- per fingerprint group, sum occurrences and recompute the time bounds,
    -- then delete the other duplicates. Done in two statements to keep the
    -- logic transparent and idempotent.
    WITH survivors AS (
        SELECT DISTINCT ON (
                   provider_id,
                   COALESCE(model_name, ''),
                   COALESCE(endpoint, ''),
                   error_type,
                   COALESCE(error_code, ''),
                   COALESCE(LEFT(error_message, 200), '')
               ) id,
               provider_id,
               model_name,
               endpoint,
               error_type,
               error_code,
               error_message
          FROM provider_error_details
          ORDER BY provider_id,
                   COALESCE(model_name, ''),
                   COALESCE(endpoint, ''),
                   error_type,
                   COALESCE(error_code, ''),
                   COALESCE(LEFT(error_message, 200), ''),
                   last_seen_at DESC,
                   id DESC
    ),
    agg AS (
        SELECT s.id AS survivor_id,
               SUM(ped.occurrences)::int                         AS sum_occurrences,
               MIN(ped.first_seen_at)                            AS min_first_seen,
               MAX(ped.last_seen_at)                             AS max_last_seen,
               COUNT(*) FILTER (WHERE ped.id <> s.id)            AS extra_rows
          FROM survivors s
            JOIN provider_error_details ped
              ON ped.provider_id                              = s.provider_id
             AND COALESCE(ped.model_name, '')                  = COALESCE(s.model_name, '')
             AND COALESCE(ped.endpoint, '')                    = COALESCE(s.endpoint, '')
             AND ped.error_type                               = s.error_type
             AND COALESCE(ped.error_code, '')                  = COALESCE(s.error_code, '')
             AND COALESCE(LEFT(ped.error_message, 200), '')    = COALESCE(LEFT(s.error_message, 200), '')
         GROUP BY s.id
    ),
    upd AS (
        UPDATE provider_error_details ped
           SET occurrences   = agg.sum_occurrences,
               first_seen_at = LEAST(ped.first_seen_at, agg.min_first_seen),
               last_seen_at  = GREATEST(ped.last_seen_at, agg.max_last_seen),
               updated_at    = NOW()
          FROM agg
         WHERE ped.id = agg.survivor_id
           AND agg.extra_rows > 0
        RETURNING ped.id
    ),
    del AS (
        DELETE FROM provider_error_details ped
         USING survivors s
         WHERE ped.provider_id                              = s.provider_id
           AND COALESCE(ped.model_name, '')                  = COALESCE(s.model_name, '')
           AND COALESCE(ped.endpoint, '')                    = COALESCE(s.endpoint, '')
           AND ped.error_type                               = s.error_type
           AND COALESCE(ped.error_code, '')                  = COALESCE(s.error_code, '')
           AND COALESCE(LEFT(ped.error_message, 200), '')    = COALESCE(LEFT(s.error_message, 200), '')
           AND ped.id                                       <> s.id
        RETURNING ped.id
    )
    SELECT count(*) INTO dup_groups FROM del;

    IF dup_groups > 0 THEN
        RAISE NOTICE 'migration 616: collapsed % duplicate rows into their fingerprint survivors', dup_groups;
    ELSE
        RAISE NOTICE 'migration 616: no legacy duplicates to collapse';
    END IF;
END
$$;

-- 添加唯一约束
-- 使用 COALESCE 处理 NULL 值，确保 (NULL, NULL) 与 (NULL, NULL) 被视为相同
-- 注意：PostgreSQL 认为 NULL != NULL，所以我们使用函数索引配合空字符串
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND indexname  = 'idx_provider_error_details_fingerprint'
    ) AND NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name   = 'provider_error_details'
          AND column_name  = 'aggregation_bucket'
    ) THEN
        EXECUTE $idx$
            CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_fingerprint
            ON provider_error_details (
                provider_id,
                COALESCE(model_name, ''),
                COALESCE(endpoint, ''),
                error_type,
                COALESCE(error_code, ''),
                COALESCE(LEFT(error_message, 200), '')
            )
        $idx$;
        RAISE NOTICE 'migration 616: created idx_provider_error_details_fingerprint';
    END IF;
END
$$;

-- 注释
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND indexname  = 'idx_provider_error_details_fingerprint'
    ) THEN
        COMMENT ON INDEX idx_provider_error_details_fingerprint IS
            '错误指纹唯一索引 - 用于 UPSERT 聚合，相同指纹的错误会合并';
    END IF;
END
$$;

-- 验证
DO $$
BEGIN
    -- 当 aggregation_bucket 已存在时，全局索引被有意省略（620 将创建
    -- tenant-scoped 索引）。其余场景下索引必须存在。
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name   = 'provider_error_details'
          AND column_name  = 'aggregation_bucket'
    ) THEN
        ASSERT (SELECT COUNT(*) FROM pg_indexes
                WHERE indexname = 'idx_provider_error_details_fingerprint') = 1,
            'Unique index not created';
    END IF;

    RAISE NOTICE '✅ Migration 616 completed: provider_error_details unique constraint handled';
END $$;