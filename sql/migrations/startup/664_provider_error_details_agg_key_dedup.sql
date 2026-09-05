-- 664: provider_error_details 聚合键去 message 碎片化（2026-09-05 round2 审计 E-#3）
--
-- 问题：620/639 的唯一指纹把 LEFT(error_message, 200) 放进聚合键，而
-- bg/provider_error_aggregator.go 的 ON CONFLICT 以同一表达式为冲突目标。
-- 同一上游错误的措辞抖动（时间戳/请求 id/重试计数等混入 message）即被拆成
-- 多个桶，occurrences / first_seen_at / last_seen_at 全部失真——本表的存在
-- 意义（按错误指纹聚合计数）被 message 自由文本瓦解。
--
-- 修复：error_message 移出唯一键、降级为"样本列"。桶身份改为
--   (COALESCE(tenant_id,''), provider_id, COALESCE(credential_id,''),
--    COALESCE(model_name,''), COALESCE(endpoint,''), error_type,
--    COALESCE(error_code,''), COALESCE(aggregation_bucket,'epoch'))
-- 同桶新样本由聚合器 UPSERT 刷新（保留最近一次措辞；occurrences 与
-- first/last_seen 仍按整桶聚合）。聚合器冲突目标同步修改（E-#3 Go 侧），
-- 二者必须同版本发布：旧二进制 + 新索引、或新二进制 + 旧索引，都会在聚合
-- tick 报 42P10（no unique or exclusion constraint matching the ON CONFLICT
-- specification）。部署接线（apply-db-revision-sequence.sh files[] 追加本
-- 文件）需与发版协同——见 round2-followup/pkg4 报告遗留清单。
--
-- 与 620/639 的 fail-closed（RAISE EXCEPTION 拦人）不同，本迁移对既有
-- message 碎片行做自动折叠：
--   1) 碎片是常态而非异常——同桶不同措辞在真实数据上几乎必然存在，
--      fail-closed 等于把本迁移变成必现的启动阻断；
--   2) 折叠语义与表语义一致：survivor 取 last_seen 最新行（message 最近），
--      occurrences = SUM(碎片行)、first_seen = MIN、last_seen = MAX，
--      同构先例是 616（本表首次建指纹时的 merge 迁移）。
--
-- 幂等：折叠 CTE 在唯一索引生效后必然 no-op；DROP INDEX IF EXISTS +
-- CREATE UNIQUE INDEX IF NOT EXISTS 可重放。
-- 无 down 迁移（660 先例：纯索引重装类）。回滚 = 恢复 639 的索引定义即可，
-- 但折叠删除的碎片行不可逆——提供 down 只会制造"可完整回滚"的错觉。
\set ON_ERROR_STOP on

BEGIN;

-- 1. 折叠既有 message 碎片行（新身份下将撞唯一索引的组合）----------
DO $do$
DECLARE
    folded bigint;
BEGIN
    WITH survivors AS (
        SELECT DISTINCT ON (
                   COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
                   COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
                   COALESCE(error_code, ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
               )
               id, tenant_id, provider_id, credential_id, model_name, endpoint,
               error_type, error_code, aggregation_bucket
        FROM public.provider_error_details
        ORDER BY COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
                 COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
                 COALESCE(error_code, ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch'),
                 last_seen_at DESC, id DESC
    ), agg AS (
        SELECT s.id AS survivor_id,
               SUM(ped.occurrences)::int              AS sum_occurrences,
               MIN(ped.first_seen_at)                 AS min_first_seen,
               MAX(ped.last_seen_at)                  AS max_last_seen,
               COUNT(*) FILTER (WHERE ped.id <> s.id) AS extra_rows
        FROM survivors s
        JOIN public.provider_error_details ped
          ON COALESCE(ped.tenant_id, '')      = COALESCE(s.tenant_id, '')
         AND ped.provider_id                  = s.provider_id
         AND COALESCE(ped.credential_id, '')  = COALESCE(s.credential_id, '')
         AND COALESCE(ped.model_name, '')     = COALESCE(s.model_name, '')
         AND COALESCE(ped.endpoint, '')       = COALESCE(s.endpoint, '')
         AND ped.error_type                   = s.error_type
         AND COALESCE(ped.error_code, '')     = COALESCE(s.error_code, '')
         AND COALESCE(ped.aggregation_bucket, TIMESTAMPTZ 'epoch')
              = COALESCE(s.aggregation_bucket, TIMESTAMPTZ 'epoch')
        GROUP BY s.id
    ), upd AS (
        UPDATE public.provider_error_details ped
           SET occurrences   = agg.sum_occurrences,
               first_seen_at = agg.min_first_seen,
               last_seen_at  = agg.max_last_seen,
               updated_at    = NOW()
          FROM agg
         WHERE ped.id = agg.survivor_id
           AND agg.extra_rows > 0
        RETURNING ped.id
    ), del AS (
        DELETE FROM public.provider_error_details ped
         USING survivors s
         WHERE COALESCE(ped.tenant_id, '')      = COALESCE(s.tenant_id, '')
           AND ped.provider_id                  = s.provider_id
           AND COALESCE(ped.credential_id, '')  = COALESCE(s.credential_id, '')
           AND COALESCE(ped.model_name, '')     = COALESCE(s.model_name, '')
           AND COALESCE(ped.endpoint, '')       = COALESCE(s.endpoint, '')
           AND ped.error_type                   = s.error_type
           AND COALESCE(ped.error_code, '')     = COALESCE(s.error_code, '')
           AND COALESCE(ped.aggregation_bucket, TIMESTAMPTZ 'epoch')
                = COALESCE(s.aggregation_bucket, TIMESTAMPTZ 'epoch')
           AND ped.id <> s.id
        RETURNING ped.id
    )
    SELECT count(*) INTO folded FROM del;

    IF folded > 0 THEN
        RAISE NOTICE '664: collapsed % message-fragmented duplicate rows into bucket survivors', folded;
    ELSE
        RAISE NOTICE '664: no message-fragmented duplicates to collapse';
    END IF;
END
$do$;

-- 2. 重装唯一索引（键去 message）------------------------------------
-- 639 之前的 620 时代索引名一并清理（仅存立于未跑 639 的中间态库）。
DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_cred_fingerprint;
DROP INDEX IF EXISTS public.idx_provider_error_details_tenant_fingerprint;
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_error_details_tenant_cred_fingerprint
ON public.provider_error_details (
    COALESCE(tenant_id, ''), provider_id, COALESCE(credential_id, ''),
    COALESCE(model_name, ''), COALESCE(endpoint, ''), error_type,
    COALESCE(error_code, ''), COALESCE(aggregation_bucket, TIMESTAMPTZ 'epoch')
);

COMMENT ON INDEX idx_provider_error_details_tenant_cred_fingerprint IS
'provider_error_details 聚合指纹（664 重装：error_message 已移出键、降级为样本列；E-#3）';

-- 3. 验证 -----------------------------------------------------------
DO $do$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'provider_error_details'
          AND indexname  = 'idx_provider_error_details_tenant_cred_fingerprint'
    ) THEN
        RAISE EXCEPTION '664 VALIDATION FAIL: idx_provider_error_details_tenant_cred_fingerprint missing';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_indexes
        WHERE schemaname = 'public'
          AND tablename  = 'provider_error_details'
          AND indexname IN ('idx_provider_error_details_tenant_fingerprint',
                            'idx_provider_error_details_fingerprint')
    ) THEN
        RAISE EXCEPTION '664 VALIDATION FAIL: legacy message-scoped fingerprint index still present';
    END IF;
    -- 键里不得再有 error_message（防止旧定义被拷回）
    IF pg_get_indexdef('idx_provider_error_details_tenant_cred_fingerprint'::regclass) LIKE '%error_message%' THEN
        RAISE EXCEPTION '664 VALIDATION FAIL: fingerprint index still keys on error_message';
    END IF;
    RAISE NOTICE '664 VALIDATION OK: error_message left the provider_error_details fingerprint';
END
$do$;

COMMIT;
