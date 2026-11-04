-- ===========================================================================
-- File:          sql/migrations/startup/949_supplier_view_binding_id.sql
-- Migration:     949
-- Purpose:       报价系统 MVP（实施规划 W1-T1 落地时发现的结构缺口）：
--                v_supplier_price_vs_baseline **末尾追加 1 列**
--                `provider_model_id`（= cmb.provider_model_id）。
--
--                为什么需要：供给池登记表 sales_supply_annotations（942）
--                按 (credential_id, provider_model_id) 键控（binding 引用，
--                AF-7 语义）；而 941 版视图不暴露 provider_model_id ⇒
--                「能卖什么」清单（视图 WHERE price_confidence='trusted'）
--                无法与登记行精确 join——按 raw_model_name 匹配在
--                「同 credential 同名双 binding」时歧义。
--
--                ★ 844/941 纪律逐字沿用：CREATE OR REPLACE VIEW 只追加
--                  **末尾**列，既有 29 列位置/类型/顺序逐字不变（844 版
--                  22 列 + 941 追加 7 市场列）；两个 LATERAL 的 JOIN 条件/
--                  ORDER BY/LIMIT 一字不改。
--
-- Down: 949_supplier_view_binding_id.down.sql（恢复 941 版 33 列定义）
-- ===========================================================================

BEGIN;

-- ★ 锁守卫；★ 实测（2026-11-05 本地 PG17 空库）：CREATE OR REPLACE VIEW
--   是元数据级操作；本文件无 CREATE INDEX / VALIDATE CONSTRAINT。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

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
           mpr.market_min_platform_id, mpr.market_min_platform_count
      FROM public.market_price_reference mpr
     WHERE mpr.dimension = 'standard'
       AND (mpr.canonical_id = mc.id
            OR (mpr.canonical_id IS NULL
                AND lower(mpr.canonical_name) = lower(mc.canonical_name)))
     ORDER BY (mpr.canonical_id IS NOT NULL) DESC
     LIMIT 1
) mpr ON true;

COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set. Migration 941 appends the market dimension: P2 market minimum (from market_price_reference, matched by canonical_id first then name, standard dimension only), the C2 market position ratio (only when supplier and market currencies are comparable), and the C3 price_confidence gate (trusted/suspect_symmetric/empty/unknown_currency, mirroring the offer_price_looks_like_placeholder health check). Market FX conversion is deliberately NOT done here — the Go scorer (sync tool) owns currency folding. Migration 949 appends provider_model_id (the binding identity, cmb.provider_model_id) so supply annotations (942, keyed by credential_id + provider_model_id) can join exactly; matching by raw name alone is ambiguous with same-name double bindings on one credential.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('949', 'v_supplier_price_vs_baseline append provider_model_id (binding identity for the 942 supply-annotation exact join; append-only 844/941 discipline, 33 prior columns untouched)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
