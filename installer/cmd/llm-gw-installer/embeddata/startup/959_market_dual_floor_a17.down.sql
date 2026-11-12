-- 959_market_dual_floor_a17.down.sql —— 回滚 A-17 双币地基：
-- 视图回 949 版 30 列定义（实测形状 = 941 的 29 列 + 949 追加的
-- provider_model_id；941 头注「26 列」为陈旧口径） → 剥 supplier_market_position 5 列 →
-- 剥 market_price_reference 17 列 → 撤 959 补种的 12 平台 → 删 schema_migrations 行。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 视图恢复 949 版（30 列 = 941 的 29 列 + 949 追加的 provider_model_id，
-- 与 949_supplier_view_binding_id.sql 的 SELECT 清单逐字一致）
--
-- ★ 回滚必须 **DROP VIEW + CREATE VIEW**，不能用 CREATE OR REPLACE VIEW：
--   PG 硬规则「CREATE OR REPLACE VIEW 不能丢列」（仓内 [M1] 同款）——
--   211 acc-pg17 实测原写法报 ERROR: cannot drop columns from view
--   （959 应用后视图 41 列 ⇒ 回滚目标是 30 列 ⇒ 少 11 列）。
--   这是仓内 E1 决策形态（view-rewriting 文件改 DROP VIEW IF EXISTS + CREATE VIEW）。
-- ★ **不加 CASCADE**：若有下游依赖，DROP 会**响亮失败**并整体回滚，
--   而不是静默毁掉依赖方（[M10] 的教训：不能靠 CASCADE 消掉）。
DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;

CREATE VIEW public.v_supplier_price_vs_baseline AS
SELECT
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(pm.canonical_raw_name, mc.canonical_name) AS canonical_name,
    COALESCE(p.id::text, '')                         AS provider_name,
    COALESCE(cmb.currency, 'USD')                    AS supplier_currency,
    COALESCE(cmb.billing_mode, 'per_token')          AS billing_mode,
    cmb.unit_price_in_per_1m                        AS supplier_in_per_1m,
    cmb.unit_price_out_per_1m                       AS supplier_out_per_1m,
    cmb.pricing_source                              AS supplier_pricing_source,
    cmb.pricing_updated_at                          AS supplier_pricing_updated_at,
    mc.baseline_price_currency                      AS baseline_currency,
    mc.baseline_input_price_per_1m                  AS baseline_in_per_1m,
    mc.baseline_output_price_per_1m                 AS baseline_out_per_1m,
    CASE
      WHEN mc.baseline_input_price_per_1m IS NULL
        OR mc.baseline_input_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_in_per_1m / mc.baseline_input_price_per_1m, 4)
    END                                             AS input_price_ratio,
    CASE
      WHEN mc.baseline_output_price_per_1m IS NULL
        OR mc.baseline_output_price_per_1m = 0
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.unit_price_out_per_1m / mc.baseline_output_price_per_1m, 4)
    END                                             AS output_price_ratio,
    (COALESCE(cmb.currency, 'USD')
       IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD'))  AS currency_comparable,
    (mc.baseline_input_price_per_1m IS NOT NULL)    AS has_baseline,
    mc.baseline_price_fetched_at                    AS baseline_fetched_at,
    mc.baseline_cache_read_price_per_1m             AS baseline_cache_read_per_1m,
    mc.baseline_cache_write_price_per_1m            AS baseline_cache_write_per_1m,
    CASE
      WHEN mc.baseline_cache_read_price_per_1m IS NULL
        OR mc.baseline_cache_read_price_per_1m = 0
        OR cmb.cache_read_price_per_1m IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.cache_read_price_per_1m / mc.baseline_cache_read_price_per_1m, 4)
    END                                             AS cache_read_price_ratio,
    CASE
      WHEN mc.baseline_cache_write_price_per_1m IS NULL
        OR mc.baseline_cache_write_price_per_1m = 0
        OR cmb.cache_write_price_per_1m IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD')
      THEN NULL
      ELSE round(cmb.cache_write_price_per_1m / mc.baseline_cache_write_price_per_1m, 4)
    END                                             AS cache_write_price_ratio,
    mpr.currency                                    AS market_ref_currency,
    mpr.market_min_in_per_1m                        AS market_ref_in_per_1m,
    mpr.market_min_out_per_1m                       AS market_ref_out_per_1m,
    mpr.market_min_platform_id                      AS market_ref_platform_id,
    mpr.market_min_platform_count                   AS market_ref_platform_count,
    CASE
      WHEN mpr.currency IS NULL
        OR COALESCE(cmb.currency, 'USD') IS DISTINCT FROM mpr.currency
        OR COALESCE(cmb.unit_price_in_per_1m, 0) + COALESCE(cmb.unit_price_out_per_1m, 0) <= 0
        OR COALESCE(mpr.market_min_in_per_1m, 0) + COALESCE(mpr.market_min_out_per_1m, 0) <= 0
      THEN NULL
      ELSE round(
        (mpr.market_min_in_per_1m + mpr.market_min_out_per_1m)
        / (cmb.unit_price_in_per_1m + cmb.unit_price_out_per_1m), 4)
    END                                             AS market_position,
    CASE
      WHEN COALESCE(cmb.currency, 'USD') NOT IN ('USD', 'CNY')
        THEN 'unknown_currency'
      WHEN COALESCE(cmb.unit_price_in_per_1m, 0) = 0
       AND COALESCE(cmb.unit_price_out_per_1m, 0) = 0
        THEN 'empty'
      WHEN cmb.unit_price_in_per_1m IS NOT NULL
       AND cmb.unit_price_in_per_1m = cmb.unit_price_out_per_1m
        THEN 'suspect_symmetric'
      ELSE 'trusted'
    END                                             AS price_confidence,
    -- ── 949 追加列（binding 身份）：回滚目标＝**949 版 30 列**，不是 941 的
    --   29 列 ⇒ 少了这一列，CREATE OR REPLACE VIEW 同样会报
    --   "cannot change name of view column"（按位置配对）⇒ 回滚也必炸。
    cmb.provider_model_id                           AS provider_model_id
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name, mc.baseline_price_currency,
           mc.baseline_input_price_per_1m, mc.baseline_output_price_per_1m,
           mc.baseline_price_fetched_at,
           mc.baseline_cache_read_price_per_1m, mc.baseline_cache_write_price_per_1m
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true
LEFT JOIN LATERAL (
    SELECT mpr.currency, mpr.market_min_in_per_1m, mpr.market_min_out_per_1m,
           mpr.market_min_platform_id, mpr.market_min_platform_count
      FROM public.market_price_reference mpr
     WHERE mpr.dimension = 'standard'
       AND (mpr.canonical_id = mc.id
            OR (mpr.canonical_id IS NULL
                AND lower(mpr.canonical_name) = lower(mc.canonical_name)))
     ORDER BY (mpr.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true;

-- 视图被重建 ⇒ 949 留下的 COMMENT 随旧视图一起消失 ⇒ 这里逐字恢复。
COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set. Migration 941 appends the market dimension: P2 market minimum (from market_price_reference, matched by canonical_id first then name, standard dimension only), the C2 market position ratio (only when supplier and market currencies are comparable), and the C3 price_confidence gate (trusted/suspect_symmetric/empty/unknown_currency, mirroring the offer_price_looks_like_placeholder health check). Market FX conversion is deliberately NOT done here — the Go scorer (sync tool) owns currency folding. Migration 949 appends provider_model_id (the binding identity, cmb.provider_model_id) so supply annotations (942, keyed by credential_id + provider_model_id) can join exactly; matching by raw name alone is ambiguous with same-name double bindings on one credential.';

ALTER TABLE public.supplier_market_position
    DROP COLUMN IF EXISTS market_position_vs_overseas,
    DROP COLUMN IF EXISTS market_position_vs_domestic,
    DROP COLUMN IF EXISTS market_ref_used,
    DROP COLUMN IF EXISTS fx_used,
    DROP COLUMN IF EXISTS vendor_ref_currency;

ALTER TABLE public.market_price_reference
    DROP COLUMN IF EXISTS vendor_list_currency,
    DROP COLUMN IF EXISTS vendor_list_cny_in_per_1m,
    DROP COLUMN IF EXISTS vendor_list_cny_out_per_1m,
    DROP COLUMN IF EXISTS vendor_list_usd_in_per_1m,
    DROP COLUMN IF EXISTS vendor_list_usd_out_per_1m,
    DROP COLUMN IF EXISTS overseas_min_in_per_1m,
    DROP COLUMN IF EXISTS overseas_min_out_per_1m,
    DROP COLUMN IF EXISTS overseas_min_platform_id,
    DROP COLUMN IF EXISTS overseas_min_platform_count,
    DROP COLUMN IF EXISTS overseas_min_cny_in_per_1m,
    DROP COLUMN IF EXISTS overseas_min_cny_out_per_1m,
    DROP COLUMN IF EXISTS domestic_min_in_per_1m,
    DROP COLUMN IF EXISTS domestic_min_out_per_1m,
    DROP COLUMN IF EXISTS domestic_min_platform_id,
    DROP COLUMN IF EXISTS domestic_min_platform_count,
    DROP COLUMN IF EXISTS domestic_min_usd_in_per_1m,
    DROP COLUMN IF EXISTS domestic_min_usd_out_per_1m;

DELETE FROM public.market_platform_registry
 WHERE platform_id IN ('aws-bedrock', 'azure-foundry', 'deepinfra',
                       'baidu-qianfan', 'bailian', 'huawei-modelarts',
                       'iflytek-spark', 'jdcloud-joybuilder', 'ppio',
                       'qiniu', 'tencent-hunyuan', 'volcengine-ark')
   AND notes LIKE '959 对拍补种%';

DELETE FROM public.schema_migrations WHERE version = '959';

COMMIT;
