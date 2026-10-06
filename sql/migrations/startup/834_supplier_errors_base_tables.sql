-- ===========================================================================
-- File:          sql/migrations/startup/834_supplier_errors_base_tables.sql
-- Migration:     834
-- Database:      llm_gateway
-- Purpose:        把 supplier_errors 族的**三张基表**纳入受追踪 startup 链，
--                修掉全新安装在 828 处硬失败。
--
-- Status:        active
-- Idempotent:    YES（全部 CREATE TABLE IF NOT EXISTS / DROP POLICY IF EXISTS /
--                 CREATE INDEX IF NOT EXISTS；不碰任何既有行）
-- Dependencies:  01-schema 基线（ensure/promote 函数已在其中，形态为 heap 版）
--                 → 699 / 703（函数时区钉扎）→ 813（分区转 heap）
--                 → 828（supplier_errors_unified 视图）
--
-- 必须在 828 之前应用。安装器按 **StartupFiles 列表顺序**执行，不按编号排序，
-- 所以本文件在列表里的位置就是它的顺序契约。
--
-- ── 立项依据（2026-10-06 集成门实测，不是推演）────────────────────────
--
-- `bash scripts/audit/run-integration-gate.sh ./bg` 的读数：
--
--     startup: applied=228 failed=1 missing=0
--     ✗ 新增未登记的启动迁移失败（1 条）:
--       - 828_supplier_errors_unified_tracked.sql ::
--         ERROR:  relation "public.supplier_errors_hot" does not exist
--
-- 门自己的措辞是「把文件与原因补进 startup_known_gaps.tsv 再重跑」——那是**记
-- 欠条**，不是修。同一份文件的头记录了上一轮 19 条缺口的**修法**，本迁移照
-- 那个先例走，而不是欠条：
--
--     "They were fixed by a different change: the pre-478 migrations were
--      added to StartupFiles, because the 01-schema baseline is a dump from
--      around 477 and is not a faithful snapshot of any single lineage."
--
-- ⇒ 处置是「把缺失的对象补进受追踪链」，不是登记豁免。
--
-- ── 缺口是怎么形成的（三条独立事实凑齐）──────────────────────────────
--
-- 1. 这三张表的**唯一定义处**是 `deploy/sql/migrations/V371__supplier_errors_hot_and_stats.sql`，
--    而 `schema_migrations` 里 V371 从未被记录（受追踪链完全没有它）。
-- 2. 升级部署路径之所以正常，是因为 `scripts/apply-db-revision-sequence.sh`
--    的 `files=(...)` **第 25 位**引用了 V371，它在建库链之前跑，把表建好了；
--    全新安装路径（`00-prereqs → 01-schema → 02-seed → 编号链`）**不跑它**。
-- 3. 828 建的是**视图**。视图在 `CREATE VIEW` 时是真校验的（不像 plpgsql 函数体
--    被 `check_function_bodies = off` 放过 —— 见 `scripts/_lib/db-init-lib.sh:225`），
--    所以 828 在缺表的库上**硬失败**，而不是安静跳过。
--
-- ★ 这解释了为什么「基线里有函数却没表」这么久没被发现：
--   `01-schema.sql` 确实有 `ensure_supplier_errors_partition`、
--   `promote_supplier_errors_hot_to_partition`、`should_be_heap` 三处引用
--   （共 15 处 supplier_errors 提及），且 813 的函数替换因为
--   `check_function_bodies = off` 照样成功 —— 只有 828 的视图创建会炸。
--
-- ── 本迁移建什么、不建什么 ──────────────────────────────────────────
--
-- 建：supplier_errors_hot（写入口）、supplier_errors（月度 RANGE 分区父表）、
--     supplier_error_stats（预聚合表）。列定义、RLS 策略、注释、索引**照抄 V371**，
--     因为 V371 是这些对象的权威定义。
--
-- 不建：任何月度分区。V371 在文件尾部急切建了「上月/本月/下月」三个分区，但
--     那三个分区是 **columnar** 的（V371 的 ensure 用 `USING columnar`），而
--     813 的职责正是把现存列存分区转 heap。在 813 之前抢先建列存分区，等于
--     让 813 多一份要转换的负担；不建则分区由 boot/小时 ensure tick 按需创建，
--     落在 813 之后的函数形态上。
--
-- 幂等与安全：对已有这些表的库，本文件整体是 no-op（IF NOT EXISTS）。它**不删、
-- 不改、不动任何既有行**，也不碰分区 —— 所以在已应用 V371 的生产库上重放它是
-- 安全的，而那正是升级部署路径。
--
-- 为什么不直接把 V371 塞进 StartupFiles：
--   ① V371 是**未受追踪**的 deploy 迁移，没有 .down.sql，而 startup 链的约定
--      要求成对的 down（`scripts/pre-commit-check.sh` 的 "has down.sql" 门）；
--   ② V371 445 行里包含一个 `supplier_errors_unified` 视图定义，而 828 要
--      `DROP VIEW IF EXISTS` 后按 20 列重建 —— 让两条迁移争同一个视图，
--      谁后跑谁说了算，是个隐患；
--   ③ V371 尾部的 VALIDATION 段会在不满足预期时 RAISE EXCEPTION，把它引进
--      「必须零失败」的启动链等于引入一个新的失败点。
--   ⇒ 重新声明三张表，多几十行，换来一个职责单一、方向明确的受追踪迁移。
-- ===========================================================================

BEGIN;

-- ═══════════════════════════════════════════════════════════════════════
-- 1. supplier_errors_hot：唯一写入入口，8h 保留后由 promote 搬进分区父表
-- ═══════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS public.supplier_errors_hot (
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

COMMENT ON TABLE public.supplier_errors_hot IS '供应商错误热表（唯一写入入口，8h 保留；V371 建表，834 入受追踪链）';
COMMENT ON COLUMN public.supplier_errors_hot.supplier      IS 'provider catalog code（低基数，禁止自由文本）';
COMMENT ON COLUMN public.supplier_errors_hot.error_type    IS 'errorsx.ErrorKind（低基数）';
COMMENT ON COLUMN public.supplier_errors_hot.stage         IS '失败阶段：preflight/connect/upstream/stream（低基数）';
COMMENT ON COLUMN public.supplier_errors_hot.error_message IS '已脱敏（errorsx.SanitizeErrorText）截断后的上游错误';

CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_occurred_at
    ON public.supplier_errors_hot (occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_supplier_credential
    ON public.supplier_errors_hot (supplier, credential_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_error_type
    ON public.supplier_errors_hot (error_type, occurred_at DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_request_id
    ON public.supplier_errors_hot (request_id);
CREATE INDEX IF NOT EXISTS idx_supplier_errors_hot_tenant
    ON public.supplier_errors_hot (tenant_id, occurred_at DESC);

-- RLS：租户隔离（与 candidate_failure_logs_hot 现行 policy 三臂逐字同型）。
-- 2026-10-06 合并对账订正：初版误用已弃用 GUC app.tenant_id（R40 census
-- 认定代码里零设置点，该臂运行时恒 NULL，非旁路会话在此表恒见零行），
-- 被 R40 census 门（db/rls_policy_census_test.go）拦下。现对齐 720 统一后
-- 的正典词汇：get_current_tenant()（app.current_tenant 的 COALESCE 封装）
-- + super_admin / bypass_rls 旁路双臂。
ALTER TABLE public.supplier_errors_hot ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.supplier_errors_hot FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_supplier_errors_hot ON public.supplier_errors_hot;
CREATE POLICY tenant_isolation_supplier_errors_hot ON public.supplier_errors_hot
    USING (
        tenant_id = get_current_tenant()
        OR current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

-- ═══════════════════════════════════════════════════════════════════════
-- 2. supplier_errors：月度 RANGE 分区父表（历史不可变）
-- ═══════════════════════════════════════════════════════════════════════

-- ⚠ 与 V371 的差异，且是有意的：V371 在这里 RAISE EXCEPTION「exists but is
--   not partitioned; manual intervention required」。那是**升级库**的担忧 ——
--   那里可能有一张同名非分区表，人工介入是对的。
--   而本迁移要覆盖**全新安装**路径，那里根本没有这张表。保留那条 RAISE
--   只会让「已存在但非分区」的库永远装不上（而那正是 V371 早于本迁移到达的
--   那些库）。所以这里改成：已存在且已分区 ⇒ NOTICE 跳过；已存在但未分区 ⇒
--   仍然大声 RAISE EXCEPTION（异常形状必须保留，那是真问题）。
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
            WHERE partrelid = 'public.supplier_errors'::regclass
        ) INTO has_partitions;

        IF has_partitions THEN
            RAISE NOTICE '834: supplier_errors already exists and is partitioned — skipping';
        ELSE
            RAISE EXCEPTION '834: public.supplier_errors exists but is NOT partitioned. '
                'A non-partitioned table of that name is a real shape difference this migration '
                'must not paper over: promote/TTL both assume a partitioned parent. '
                'Manual intervention required.';
        END IF;
    ELSE
        -- id 为普通 bigint（对齐 V359 父表模式）：promote 从 hot 表携带 id
        -- 直插，父表不自行生成（GENERATED ALWAYS 会拒绝显式插入并破坏
        -- promote 的 copy-delete-insert 契约）。
        EXECUTE $sql$
            CREATE TABLE public.supplier_errors (
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
        RAISE NOTICE '834: created partitioned parent public.supplier_errors';
    END IF;
END
$do$;

COMMENT ON TABLE public.supplier_errors IS '供应商错误历史（月度 RANGE 分区，append-only；V371 建表，834 入受追踪链）';

-- ═══════════════════════════════════════════════════════════════════════
-- 3. supplier_error_stats：预聚合（minute/hour/day）
-- ═══════════════════════════════════════════════════════════════════════

CREATE TABLE IF NOT EXISTS public.supplier_error_stats (
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
    -- UPSERT 键刻意保持 (stat_time, granularity, supplier, credential_id,
    -- error_type, model) 不变：把 is_retryable/stage 提为维度键会把行数乘上
    -- retryable×stage 组合、并改变 hour/day 二次 rollup 的分组语义。
    retryable_count integer NOT NULL DEFAULT 0,
    -- stage_counts：桶内 stage→计数 map（低基数受控词表，''=未知在读端映射为
    -- unknown；jsonb 而非固定列：新增 stage 不需要 DDL）。hour/day rollup 按
    -- jsonb_each 合并求和（bg/supplier_error_stats_aggregator.go）。
    stage_counts    jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_rate      numeric(7, 4) GENERATED ALWAYS AS (
        CASE WHEN total_requests > 0
             THEN (error_count::numeric / total_requests) * 100
             ELSE 0 END
    ) STORED,
    aggregated_at   timestamp with time zone NOT NULL DEFAULT now(),
    CONSTRAINT uq_supplier_error_stats_bucket
        UNIQUE (stat_time, granularity, supplier, credential_id, error_type, model)
);

-- E-#6 增列的自愈守卫：若某库已应用过增列前的 V371（CREATE TABLE IF NOT
-- EXISTS 会整段跳过），重放本文件时由此补齐两列。NOT NULL DEFAULT 对既有
-- 行回填 0/'{}'（= 未知桶，读端按 0 呈现），不改写历史语义。
ALTER TABLE public.supplier_error_stats ADD COLUMN IF NOT EXISTS retryable_count integer NOT NULL DEFAULT 0;
ALTER TABLE public.supplier_error_stats ADD COLUMN IF NOT EXISTS stage_counts jsonb NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN public.supplier_error_stats.retryable_count IS
    '桶内 is_retryable=true 行数（E-#6；可重试率 = retryable_count/error_count）';
COMMENT ON COLUMN public.supplier_error_stats.stage_counts IS
    '桶内 stage→计数 jsonb map（E-#6；低基数词表，空串=未知由读端映射 unknown）';

CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_time_granularity
    ON public.supplier_error_stats (stat_time DESC, granularity);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_supplier
    ON public.supplier_error_stats (supplier, stat_time DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_credential
    ON public.supplier_error_stats (credential_id, stat_time DESC);
CREATE INDEX IF NOT EXISTS idx_supplier_error_stats_error_type
    ON public.supplier_error_stats (error_type, stat_time DESC);

ALTER TABLE public.supplier_error_stats ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.supplier_error_stats FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation_supplier_error_stats ON public.supplier_error_stats;
-- 预聚合表不含真实租户维度（credential_id=0 表示全凭据聚合），policy 刻意
-- USING (true) 全放行 —— 聚合器/admin 读端均无需 tenant GUC，也无 42501 风险；
-- 与 hot/父表不同，无需 bypass 旁路。
CREATE POLICY tenant_isolation_supplier_error_stats ON public.supplier_error_stats
    USING (true);

COMMENT ON TABLE public.supplier_error_stats IS '供应商错误预聚合（minute/hour/day；UPSERT 唯一键防重；V371 建表，834 入受追踪链）';
COMMENT ON CONSTRAINT uq_supplier_error_stats_bucket ON public.supplier_error_stats IS
    '维度列 NOT NULL DEFAULT 空串（PG UNIQUE 对 NULL 不去重）；credential_id=0 表示全凭据聚合。';

-- ═══════════════════════════════════════════════════════════════════════
-- 4. 落地自证：三张表都在，且父表确实是分区表
-- ═══════════════════════════════════════════════════════════════════════

DO $verify$
DECLARE
    n_part  integer;
    missing text;
BEGIN
    SELECT count(*) INTO n_part
      FROM pg_partitioned_table
     WHERE partrelid = 'public.supplier_errors'::regclass;
    IF n_part <> 1 THEN
        RAISE EXCEPTION '834 self-check: public.supplier_errors is not a partitioned table — '
            'the parent was not created, and 828 will fail on the very next step';
    END IF;

    FOR missing IN
        SELECT t FROM unnest(ARRAY['supplier_errors','supplier_errors_hot','supplier_error_stats']) AS t
         WHERE to_regclass('public.' || t) IS NULL
    LOOP
        RAISE EXCEPTION '834 self-check: public.% does not exist after this migration', missing;
    END LOOP;

    RAISE NOTICE '834 self-check: supplier_errors (partitioned) + supplier_errors_hot + '
        'supplier_error_stats all present; no monthly partitions created (ensure tick owns those)';
END
$verify$;

COMMIT;
