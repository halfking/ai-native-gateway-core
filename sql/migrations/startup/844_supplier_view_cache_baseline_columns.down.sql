-- 844_supplier_view_cache_baseline_columns.down.sql
-- 回滚 844：把 v_supplier_price_vs_baseline **退回 826 的投影**（不含两个 cache 基准价）。
--
-- ⚠ 本回滚是**真实回滚**，不是「只删新机制」：
--   它会删掉两个 cache 倍率列，而它们正是 844 之后唯一一处
--   「缓存基准价进入告警通路」的地方。
--   ⇒ 回滚后缓存基准价重新变成只写不读（826 的原始状态）。
--   这是刻意保留的事实：844 的效果就是那几个列。
--
-- ⚠ 与别的 down 不同，这里**故意不动** credential_model_bindings 上
--   844 用 ADD COLUMN IF NOT EXISTS 补出来的两个 cache 价列。
--   理由：它们不是 844「建的」——模型 398 重建 model_offers 时就 SELECT 了它们，
--   在受追踪链之前的基线库里早就存在。删掉会破坏本回滚**之前**就存在的对象。
--   ⇒ 判据：只回滚「投影」，不回滚「补列」。补列留在库里是无害的
--     （没有视图读它们，等同 844 之前）。
--
-- 幂等：DROP VIEW IF EXISTS + CREATE VIEW，重复应用同结果。
--
-- ⚠ 实测（一次性 PG 17.11，本轮）记一条机制：
--   **CREATE OR REPLACE VIEW 不能删除列**。第一版 down 用的就是它，
--   实跑直接报 `无法从视图中删除列`，而视图的 4 个 cache 列**一个没少** ——
--   也就是那条 down 是个**假回滚**：它报成功（psql 无 -v ON_ERROR_STOP 时
--   rc 仍是 0），却什么都没撤。
--   ⇒ 删列只能 DROP VIEW + CREATE VIEW。代价是这条窗口里视图短暂不存在，
--     所以用 IF EXISTS + 紧随其后的 CREATE，且**不放进单事务里**：
--     BEGIN 里 DROP 后 CREATE 失败会把整条回滚，等于撤不掉 844（更糟）。
--     这里保持与 826/398 一致的「IF EXISTS + CREATE」幂等形态。
--
-- Related: 844_supplier_view_cache_baseline_columns.sql
--           sql/migrations/startup/826_model_baseline_price.sql

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
    mc.baseline_price_fetched_at                    AS baseline_fetched_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
-- 条件 / ORDER BY / LIMIT 1 与 826、844 逐字一致（826 记录过 OR 条件会让一条
-- 绑定变三行的真实事故，去重修法不能被任何回滚移走）。
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name, mc.baseline_price_currency,
           mc.baseline_input_price_per_1m, mc.baseline_output_price_per_1m,
           mc.baseline_price_fetched_at
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true;

COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set. Rolled back from 844: the two cache baseline columns and their ratios are gone, so cache pricing has no drift coverage.';