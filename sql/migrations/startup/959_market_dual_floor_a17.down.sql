-- 959_market_dual_floor_a17.down.sql —— 回滚 A-17 双币地基：
-- 视图回 949 版 30 列定义（941 的 29 列 + 949 追加的 provider_model_id） → 剥 supplier_market_position 5 列 →
-- 剥 market_price_reference 17 列 → 撤 959 补种的 12 平台 → 删 schema_migrations 行。

BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- 视图恢复 949 版（30 列 = 941 的 29 + 949 追加的 provider_model_id；949_supplier_view_binding_id.sql 逐字）
CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline AS
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
    -- 倍率：供应商实付 / 原厂标准。>1 = 比原厂贵，<1 = 有折扣。
    -- 只有在**币种相同**时才算得出来，所以下面同时给出可比性标记。
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
    -- 币种不一致时必须显眼：拿 CNY 的供应商价去比 USD 的基准价，出来的
    -- 偏差是纯噪声，但它在报表里长得和真偏差一模一样。
    (COALESCE(cmb.currency, 'USD')
       IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency, 'USD'))  AS currency_comparable,
    (mc.baseline_input_price_per_1m IS NOT NULL)    AS has_baseline,
    mc.baseline_price_fetched_at                    AS baseline_fetched_at,
    -- ── 844：两个缓存基准价 ─────────────────────────────────────────────
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
    -- ── 941：市场口径（P2 市场最低 + C2 位势 + C3 置信度门） ─────────────
    mpr.currency                                    AS market_ref_currency,
    mpr.market_min_in_per_1m                        AS market_ref_in_per_1m,
    mpr.market_min_out_per_1m                       AS market_ref_out_per_1m,
    mpr.market_min_platform_id                      AS market_ref_platform_id,
    mpr.market_min_platform_count                   AS market_ref_platform_count,
    -- C2 = 市场最低价 / 供应商价（blended）。>1 = 我们比市场便宜。
    -- 只在币种可比且两侧都有正价时算；否则 NULL（与 baseline 倍率同纪律）。
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
    -- C3 价格置信度门（不是分）：口径镜像 offer_price_looks_like_placeholder
    -- （对称正价=占位候选 / 0/0=empty 非缺陷 / 币种不在已知集=unknown_currency）。
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
    -- ── 949 新增：binding 身份（供给登记 942 的精确 join 键） ─────────────
    cmb.provider_model_id                           AS provider_model_id
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
-- ★ 这个 JOIN 的条件、ORDER BY、LIMIT 1 与 826/844/941 **逐字一致**，一字未改。
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
-- ★ 941 的市场 LATERAL（dedup：canonical_id 解析优先，AF-8），逐字保留。
LEFT JOIN LATERAL (
    SELECT mpr.currency, mpr.market_min_in_per_1m, mpr.market_min_out_per_1m,
           mpr.market_min_platform_id, mpr.market_min_platform_count,
           mpr.overseas_min_in_per_1m, mpr.overseas_min_out_per_1m,
           mpr.overseas_min_platform_id, mpr.overseas_min_platform_count,
           mpr.domestic_min_in_per_1m, mpr.domestic_min_out_per_1m,
           mpr.domestic_min_platform_id, mpr.domestic_min_platform_count
      FROM public.market_price_reference mpr
     WHERE mpr.dimension = 'standard'
       AND (mpr.canonical_id = mc.id
            OR (mpr.canonical_id IS NULL
                AND lower(mpr.canonical_name) = lower(mc.canonical_name)))
     ORDER BY (mpr.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true;

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
