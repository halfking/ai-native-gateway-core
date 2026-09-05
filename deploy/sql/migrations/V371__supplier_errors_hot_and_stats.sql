-- =============================================================================
-- V371__supplier_errors_hot_and_stats.sql
-- 2026-09-05: 供应商错误唯一事实源（审计闭环1）— supplier_errors_hot +
--             月度 columnar 分区 + supplier_error_stats 预聚合
--
-- 背景（docs/audit-2026-09-05-ir-storage-provider.md 未闭环项）：
--   docs/prompts/task3-error-display-optimization.md 设计的
--   supplier_errors_hot / supplier_error_stats 在 Go 生产写入链路中不存在；
--   dashboard 实际查询 session_module_executions_hot 等聚合，
--   供应商/凭据/模型/HTTP code/retryable 维度不完整。
--
-- 设计（对齐 task3 设计稿 + V359/627 治理模板）：
--   1. supplier_errors_hot：heap（fillfactor=90），唯一写入入口，
--      由 CandidateFailureWriter 统一写入（每个失败候选一行），
--      默认 8h 保留，由 promote_supplier_errors_hot_to_partition
--      批量迁移到月度 columnar 分区。
--   2. supplier_errors：PARTITION BY RANGE (occurred_at) 父表，
--      月度子分区 USING columnar（zstd / stripe 150000 / chunk_group 10000），
--      历史分区不可 UPDATE/DELETE（citus_columnar 访问方法天然拒绝）。
--   3. supplier_errors_unified：hot ∪ historical 视图（security_invoker），
--      admin 读端唯一入口。
--   4. supplier_error_stats：预聚合表，UNIQUE(stat_time, granularity,
--      supplier, credential_id, error_type, model) 防重复聚合；
--      维度列 NOT NULL DEFAULT '' 使唯一约束在"未知维度"下仍然成立
--      （PostgreSQL UNIQUE 对 NULL 不去重）。
--
-- 部署：低峰窗口，不停写（本迁移为新建表，无历史数据迁移）。
-- =============================================================================

\set ON_ERROR_STOP on

\echo '=== V371: supplier_errors_hot + 月度 columnar 分区 + 预聚合 ==='

-- ---------------------------------------------------------------------------
-- 1. supplier_errors_hot（heap，唯一写入入口）
-- ---------------------------------------------------------------------------
\echo '--- 1. CREATE TABLE supplier_errors_hot ---'
CREATE TABLE IF NOT EXISTS supplier_errors_hot (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    occurred_at       timestamp with time zone DEFAULT now() NOT NULL,
    request_id        text NOT NULL,
    trace_id          text,
    tenant_id         text DEFAULT 'default'::text NOT NULL,
    session_id        text,
    provider_id       integer NOT NULL,
    -- supplier = provider catalog code（openai/anthropic/gemini…）；
    -- 未知时存空串而非 NULL，保证维度聚合唯一键成立。
    supplier          text DEFAULT ''::text NOT NULL,
    credential_id     bigint NOT NULL,
    model             text NOT NULL,
    attempt_seq       integer DEFAULT 0 NOT NULL,
    -- error_type = errorsx.ErrorKind（rate_limit/timeout/auth…）。
    error_type        text NOT NULL,
    -- error_code = 供应商 API 错误码（无则空）；http_status = 上游 HTTP 状态。
    error_code        text DEFAULT ''::text NOT NULL,
    http_status       integer,
    error_message     text,
    is_retryable      boolean DEFAULT false NOT NULL,
    stage             text DEFAULT ''::text NOT NULL,
    latency_ms        integer,
    affected_users    integer DEFAULT 1 NOT NULL,
    request_metadata  jsonb
);

-- 列注释（低基数约束：error_type/stage/supplier 必须是受控词表，禁止自由文本）
COMMENT ON TABLE  supplier_errors_hot IS '供应商错误热表（唯一写入入口，8h 保留；V371）';
COMMENT ON COLUMN supplier_errors_hot.supplier       IS 'provider catalog code（低基数，禁止自由文本）';
COMMENT ON COLUMN supplier_errors_hot.error_type     IS 'errorsx.ErrorKind（低基数）';
COMMENT ON COLUMN supplier_errors_hot.stage          IS '失败阶段：preflight/connect/upstream/stream（低基数）';
COMMENT ON COLUMN supplier_errors_hot.error_message  IS '已脱敏（errorsx.SanitizeErrorText）截断后的上游错误';

CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_occurred_at
    ON supplier_errors_hot (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_supplier_credential
    ON supplier_errors_hot (supplier, credential_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_error_type
    ON supplier_errors_hot (error_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_request_id
    ON supplier_errors_hot (request_id);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_tenant
    ON supplier_errors_hot (tenant_id, occurred_at DESC);

-- RLS：租户隔离（与 candidate_failure_logs_hot 同模式）
-- 2026-09-05 审计 E-#1：FORCE RLS + USING (tenant_id = current_setting(..., true))
-- 在未设置 app.tenant_id 时比较为 NULL → 全链路（写入/聚合/admin 读端，
-- Go 侧无任何 app.tenant_id set_config）42501 或恒空。补
-- app.bypass_rls 旁路（对齐 V367 promote 场景既有模式）。
ALTER TABLE supplier_errors_hot ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier_errors_hot FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_supplier_errors_hot ON supplier_errors_hot;
CREATE POLICY tenant_isolation_supplier_errors_hot ON supplier_errors_hot
    USING (
        tenant_id = current_setting('app.tenant_id', true)
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- ---------------------------------------------------------------------------
-- 2. supplier_errors（月度 columnar 分区父表，历史不可变）
-- ---------------------------------------------------------------------------
\echo '--- 2. CREATE partitioned parent supplier_errors ---'
DO $do$
DECLARE
    has_table      boolean;
    has_partitions boolean;
BEGIN
    SELECT EXISTS (
        SELECT 1 FROM pg_class
        WHERE relname = 'supplier_errors'
          AND relnamespace = 'public'::regnamespace
    ) INTO has_table;

    IF has_table THEN
        SELECT EXISTS (
            SELECT 1 FROM pg_partitioned_table
            WHERE partrelid = 'supplier_errors'::regclass
        ) INTO has_partitions;

        IF has_partitions THEN
            RAISE NOTICE 'V371: supplier_errors already partitioned, skipping';
        ELSE
            RAISE EXCEPTION 'V371: supplier_errors exists but is not partitioned (heap?); manual intervention required';
        END IF;
    ELSE
        EXECUTE $sql$
            -- id 为普通 bigint（对齐 V359 父表模式）：promote 从 hot 表
            -- 携带 id 直插，父表不自行生成（GENERATED ALWAYS 会拒绝显式
            -- 插入并破坏 promote 的 copy-delete-insert 契约）。
            CREATE TABLE supplier_errors (
                id                bigint,
                occurred_at       timestamp with time zone DEFAULT now() NOT NULL,
                request_id        text NOT NULL,
                trace_id          text,
                tenant_id         text DEFAULT 'default'::text NOT NULL,
                session_id        text,
                provider_id       integer NOT NULL,
                supplier          text DEFAULT ''::text NOT NULL,
                credential_id     bigint NOT NULL,
                model             text NOT NULL,
                attempt_seq       integer DEFAULT 0 NOT NULL,
                error_type        text NOT NULL,
                error_code        text DEFAULT ''::text NOT NULL,
                http_status       integer,
                error_message     text,
                is_retryable      boolean DEFAULT false NOT NULL,
                stage             text DEFAULT ''::text NOT NULL,
                latency_ms        integer,
                affected_users    integer DEFAULT 1 NOT NULL,
                request_metadata  jsonb
            ) PARTITION BY RANGE (occurred_at)
        $sql$;
        RAISE NOTICE 'V371: created partitioned parent supplier_errors';
    END IF;
END
$do$;

COMMENT ON TABLE supplier_errors IS '供应商错误历史（月度 columnar 分区，append-only：columnar AM 拒绝 UPDATE/DELETE；V371）';

-- ---------------------------------------------------------------------------
-- 3. ensure_supplier_errors_partition（沿用 392/V359 模板，幂等）
-- ---------------------------------------------------------------------------
\echo '--- 3. CREATE ensure_supplier_errors_partition ---'
DROP FUNCTION IF EXISTS ensure_supplier_errors_partition(timestamp with time zone);
CREATE OR REPLACE FUNCTION ensure_supplier_errors_partition(target_ts timestamp with time zone)
RETURNS text
LANGUAGE plpgsql
AS $$
DECLARE
    month_start    date := date_trunc('month', target_ts)::date;
    month_end      date := (date_trunc('month', target_ts) + interval '1 month')::date;
    partition_name text := 'supplier_errors_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class
                   WHERE relname = partition_name
                     AND relnamespace = 'public'::regnamespace) THEN
        -- 裸 USING columnar（对齐 V359 模板）：citus_columnar 11.2+ 的
        -- 压缩/stripe/chunk 参数走 columnar.* GUC（全局默认 zstd/
        -- 150000/10000），不再接受 WITH(...) reloption。
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF supplier_errors
             FOR VALUES FROM (%L) TO (%L) USING columnar',
            partition_name, month_start, month_end
        );
        RAISE NOTICE 'ensure_supplier_errors_partition: created % as columnar', partition_name;
    ELSE
        -- 幂等：确保既有分区保持 columnar（历史分区不可变语义）
        PERFORM enforce_columnar_partition(partition_name, 'supplier_errors');
    END IF;
    RETURN partition_name;
END;
$$;

COMMENT ON FUNCTION ensure_supplier_errors_partition(timestamp with time zone) IS
'Ensure monthly columnar partition for supplier_errors (created by V371, 2026-09-05).
Mirrors V359 ensure_* pattern; idempotent.';

-- 当月 + 上下月分区（启动可写窗口）
SELECT ensure_supplier_errors_partition((date_trunc('month', NOW()) - interval '1 month')::timestamp);
SELECT ensure_supplier_errors_partition(date_trunc('month', NOW())::timestamp);
SELECT ensure_supplier_errors_partition((date_trunc('month', NOW()) + interval '1 month')::timestamp);

-- ---------------------------------------------------------------------------
-- 4. RLS：父表
--    2026-09-05 审计 E-#1：promote 以应用角色写父表、admin 读端经
--    supplier_errors_unified（security_invoker）扫父表，同样需要
--    app.bypass_rls 旁路，否则 FORCE RLS 下历史侧整段不可见/不可写。
-- ---------------------------------------------------------------------------
ALTER TABLE supplier_errors ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier_errors FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_supplier_errors ON supplier_errors;
CREATE POLICY tenant_isolation_supplier_errors ON supplier_errors
    USING (
        tenant_id = current_setting('app.tenant_id', true)
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- ---------------------------------------------------------------------------
-- 5. supplier_errors_unified 视图（hot ∪ historical，admin 读端唯一入口）
-- ---------------------------------------------------------------------------
\echo '--- 5. CREATE VIEW supplier_errors_unified ---'
CREATE OR REPLACE VIEW public.supplier_errors_unified AS
SELECT
    'hot'::text AS source,
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors_hot
UNION ALL
SELECT
    'historical'::text AS source,
    id, occurred_at, request_id, trace_id, tenant_id, session_id,
    provider_id, supplier, credential_id, model, attempt_seq,
    error_type, error_code, http_status, error_message,
    is_retryable, stage, latency_ms, affected_users, request_metadata
FROM public.supplier_errors;

ALTER VIEW public.supplier_errors_unified SET (security_invoker = true);

-- ---------------------------------------------------------------------------
-- 6. promote 函数（hot → 月度 columnar 分区；默认 8h 保留 + 5000 批次）
-- ---------------------------------------------------------------------------
\echo '--- 6. CREATE promote_supplier_errors_hot_to_partition ---'
DROP FUNCTION IF EXISTS promote_supplier_errors_hot_to_partition(interval, int);
CREATE OR REPLACE FUNCTION promote_supplier_errors_hot_to_partition(
    p_retention interval DEFAULT '8 hours',
    p_batch_size int DEFAULT 5000
)
RETURNS bigint
LANGUAGE plpgsql AS $$
DECLARE
    moved bigint := 0;
BEGIN
    -- 参数守卫（与 656 模板一致：错误直接上抛，由调用方记录）
    IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN
        RAISE EXCEPTION 'p_retention must be positive';
    END IF;
    IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN
        RAISE EXCEPTION 'p_batch_size must be between 1 and 50000';
    END IF;

    -- 确保目标分区存在（遍历 cold 行的月份）
    PERFORM ensure_supplier_errors_partition(m)
    FROM (
        SELECT DISTINCT date_trunc('month', occurred_at) AS m
        FROM supplier_errors_hot
        WHERE occurred_at < statement_timestamp() - p_retention
        LIMIT 12
    ) months;

    -- 2026-09-05 审计 D-2#2：改为 656 模板式单条 data-modifying CTE。
    -- 旧实现（temp table 复制 → 异常块外 DELETE → INSERT ... EXCEPTION 吞错）
    -- 是 602 迁移注释记录过的真实事故模式：plpgsql EXCEPTION 子块只回滚
    -- 子事务（INSERT），外层 DELETE 照样提交，INSERT 一旦失败该批错误明细
    -- 即静默丢失，且 RAISE WARNING 还宣称 rows preserved in hot table。
    -- 现版本 FOR UPDATE SKIP LOCKED → DELETE...RETURNING（显式列，防
    -- schema drift 按位错配）→ INSERT，单语句原子：任何失败整体回滚、
    -- 错误自然上抛（Go 侧 recordPromoteFailure 记录），不再吞 EXCEPTION。
    WITH batch AS (
        SELECT id FROM supplier_errors_hot
        WHERE occurred_at < statement_timestamp() - p_retention
        ORDER BY occurred_at, id
        LIMIT p_batch_size
        FOR UPDATE SKIP LOCKED
    ), moved_rows AS (
        DELETE FROM supplier_errors_hot h USING batch b
        WHERE h.id = b.id
        RETURNING h.id, h.occurred_at, h.request_id, h.trace_id, h.tenant_id,
                  h.session_id, h.provider_id, h.supplier, h.credential_id,
                  h.model, h.attempt_seq, h.error_type, h.error_code,
                  h.http_status, h.error_message, h.is_retryable, h.stage,
                  h.latency_ms, h.affected_users, h.request_metadata
    ), inserted AS (
        INSERT INTO supplier_errors (
            id, occurred_at, request_id, trace_id, tenant_id,
            session_id, provider_id, supplier, credential_id,
            model, attempt_seq, error_type, error_code,
            http_status, error_message, is_retryable, stage,
            latency_ms, affected_users, request_metadata)
        SELECT id, occurred_at, request_id, trace_id, tenant_id,
               session_id, provider_id, supplier, credential_id,
               model, attempt_seq, error_type, error_code,
               http_status, error_message, is_retryable, stage,
               latency_ms, affected_users, request_metadata
        FROM moved_rows
        RETURNING id
    )
    SELECT count(*) INTO moved FROM inserted;

    RETURN moved;
END;
$$;

COMMENT ON FUNCTION promote_supplier_errors_hot_to_partition(interval, int) IS
'Promotes cold rows from supplier_errors_hot to monthly columnar partitions
(default retention 8h, batch 5000). Atomic single data-modifying CTE
(FOR UPDATE SKIP LOCKED -> DELETE RETURNING -> INSERT; 2026-09-05 audit
D-2#2 replaced the temp-table + EXCEPTION body that could silently drop a
batch on INSERT failure). Registered in
admin.data_lifecycle_hot_partition.hotPromoteTableMap. Created by V371 (2026-09-05).';

-- ---------------------------------------------------------------------------
-- 7. supplier_error_stats 预聚合表
-- ---------------------------------------------------------------------------
\echo '--- 7. CREATE TABLE supplier_error_stats ---'
CREATE TABLE IF NOT EXISTS supplier_error_stats (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    stat_time       timestamp with time zone NOT NULL,
    granularity     text NOT NULL,
    supplier        text DEFAULT ''::text NOT NULL,
    credential_id   bigint DEFAULT 0 NOT NULL,
    error_type      text DEFAULT ''::text NOT NULL,
    model           text DEFAULT ''::text NOT NULL,
    error_count     integer NOT NULL,
    unique_requests integer NOT NULL,
    affected_users  integer NOT NULL,
    success_count   integer NOT NULL DEFAULT 0,
    total_requests  integer NOT NULL,
    error_rate      numeric(7, 4) GENERATED ALWAYS AS (
        CASE WHEN total_requests > 0
             THEN (error_count::numeric / total_requests) * 100
             ELSE 0 END
    ) STORED,
    aggregated_at   timestamp with time zone NOT NULL DEFAULT now(),
    -- 防重复聚合：同一 (时间桶, 粒度, 维度组合) 仅一行，重复聚合走 upsert。
    CONSTRAINT uq_supplier_error_stats_bucket
        UNIQUE (stat_time, granularity, supplier, credential_id, error_type, model)
);

CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_time_granularity
    ON supplier_error_stats (stat_time DESC, granularity);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_supplier
    ON supplier_error_stats (supplier, stat_time DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_credential
    ON supplier_error_stats (credential_id, stat_time DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_error_type
    ON supplier_error_stats (error_type, stat_time DESC);

ALTER TABLE supplier_error_stats ENABLE ROW LEVEL SECURITY;
ALTER TABLE supplier_error_stats FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_supplier_error_stats ON supplier_error_stats;
-- 2026-09-05 审计 E-#1 备注：预聚合表不含真实租户维度（credential_id=0
-- 表示全凭据聚合），policy 刻意 USING (true) 全放行——聚合器/admin 读端
-- 均无需 tenant GUC，也无 42501 风险；与 hot/父表不同，无需 bypass 旁路。
CREATE POLICY tenant_isolation_supplier_error_stats ON supplier_error_stats
    USING (true);

COMMENT ON TABLE supplier_error_stats IS '供应商错误预聚合（minute/hour/day；UPSERT 唯一键防重；V371）';
COMMENT ON CONSTRAINT uq_supplier_error_stats_bucket ON supplier_error_stats IS
'维度列 NOT NULL DEFAULT ''''（PG UNIQUE 对 NULL 不去重）；credential_id=0 表示全凭据聚合。';

-- ---------------------------------------------------------------------------
-- 8. 验证迁移
-- ---------------------------------------------------------------------------
\echo '--- 8. VALIDATION ---'
DO $do$
DECLARE
    has_hot      boolean;
    has_part     boolean;
    has_view     boolean;
    has_fn       boolean;
    has_stats    boolean;
    has_uq       boolean;
    has_policy   boolean;
BEGIN
    SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'supplier_errors_hot') INTO has_hot;
    SELECT EXISTS (SELECT 1 FROM pg_partitioned_table WHERE partrelid = 'supplier_errors'::regclass) INTO has_part;
    SELECT EXISTS (SELECT 1 FROM pg_views WHERE viewname = 'supplier_errors_unified') INTO has_view;
    SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'promote_supplier_errors_hot_to_partition') INTO has_fn;
    SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = 'supplier_error_stats') INTO has_stats;
    SELECT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'supplier_error_stats'::regclass
          AND conname = 'uq_supplier_error_stats_bucket'
    ) INTO has_uq;
    SELECT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE schemaname='public' AND tablename='supplier_errors_hot'
          AND policyname='tenant_isolation_supplier_errors_hot'
    ) INTO has_policy;

    IF NOT has_hot    THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: supplier_errors_hot missing'; END IF;
    IF NOT has_part   THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: supplier_errors not partitioned'; END IF;
    IF NOT has_view   THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: supplier_errors_unified view missing'; END IF;
    IF NOT has_fn     THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: promote fn missing'; END IF;
    IF NOT has_stats  THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: supplier_error_stats missing'; END IF;
    IF NOT has_uq     THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: stats unique constraint missing'; END IF;
    IF NOT has_policy THEN RAISE EXCEPTION 'V371 VALIDATION FAIL: RLS policy missing'; END IF;

    RAISE NOTICE 'V371 VALIDATION OK: hot=%, partitioned=%, view=%, promote_fn=%, stats=%, uq=%, rls=%',
        has_hot, has_part, has_view, has_fn, has_stats, has_uq, has_policy;
END
$do$;
