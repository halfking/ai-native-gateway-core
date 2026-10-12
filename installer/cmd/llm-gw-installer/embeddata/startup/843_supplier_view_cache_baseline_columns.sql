-- 842_supplier_view_cache_baseline_columns.sql
-- 2026-10-07：让 v_supplier_price_vs_baseline **导出两个缓存基准价**。
--
-- 背景（这是一个「写入方齐全、读取方为零」的实例）：
--   models_canonical 有 baseline_cache_read_price_per_1m /
--   baseline_cache_write_price_per_1m（826 建列，90/91 行加了非负 CHECK），
--   credential_model_bindings 有对应的 cache_read_price_per_1m /
--   cache_write_price_per_1m（398 建）。
--   两侧都有值，**比得出来**。
--
--   但 826 建的 v_supplier_price_vs_baseline 只投影了 in/out 两个基准价：
--     · 主 SELECT 里只有 baseline_in_per_1m / baseline_out_per_1m；
--     · 那个 LATERAL 子查询也只 SELECT 了 6 列，
--       **baseline_cache_* 压根没进子查询**（本迁移要同时补这两处）。
--   ⇒ bg/routing_health_checks.go 的 supplier_price_drift 的 WHERE 与 SELECT
--     全部走视图列 ⇒ 缓存基准价即使在 SSOT 里正确、在库里非空，
--     也**永远进不了任何告警**。
--
-- 与本仓已记录的两处同族：「台账只写不读」、「币种不一致让整条绑定脱离监控」。
--
-- 为什么现在修（2026-10-07 的人工拍板给了它一个明确归属）：
--   基准价在成本核算里的角色已定为**「合理性下限」告警，不参与金额计算**。
--   ⇒ 「基准价只写不读」不再是一个中性的观察缺口，而是一个**功能缺失**：
--     被指定为告警依据的四个价里，有两个（缓存读/写）根本没接进告警通路。
--
-- ★ 为什么不用 CREATE OR REPLACE 原地改 826：
--   826 已在生产上线（台账 applied+verified），改它的文件内容会与
--   幂等通道按 sha 的台账记账冲突（内容变了但编号不变 ⇒ 每次部署重放）。
--   本仓惯例是新开一个 re-assert 迁移（813 → 836 同型）。
--
-- 安全性：
--   · CREATE OR REPLACE VIEW **只能在末尾追加列**，不能改中间列的类型/顺序。
--     本迁移把 4 个新列全部追加在 baseline_fetched_at 之后 ⇒
--     既有列的位置、类型、顺序**逐字不变**，消费方（8 个 Go 文件，无 SELECT *）
--     不受影响。
--   · LATERAL 子查询**只加两列 SELECT**，JOIN 条件、ORDER BY、LIMIT 1
--     一字不改 ⇒ 行数与去重行为与 826 完全一致（826 的注释记录过
--     「OR 两侧各命中不同 canonical 行会把一条绑定变成三行」，那个修法
--     不能被本迁移动到）。
--
-- 幂等：先补列（IF NOT EXISTS）+ CREATE OR REPLACE VIEW，重复应用同结果。
-- 回滚：842_supplier_view_cache_baseline_columns.down.sql
-- Related: sql/migrations/startup/826_model_baseline_price.sql
--           bg/routing_health_checks.go（supplier_price_drift）

BEGIN;

-- ★ 前置：先把 credential_model_bindings 的两个缓存价列**补出来**。
--
--   为什么必须在本迁移里补，而不能假设它们已经存在（本轮实测踩到）：
--   视图定义里**不能引用不存在的列**——CREATE OR REPLACE VIEW 会直接
--   42703 失败，整条部署挂掉。实测（一次性 PG 17.11，本轮）：
--     在只缺这两列的最小 schema 上跑本文件 ⇒
--       ERROR: 字段 cmb.cache_read_price_per_1m 不存在  (42703)
--
--   而这两个列**没有任何受追踪迁移建过**：全仓 `ADD COLUMN ... cache_read`
--   只命中 826，而那是 `baseline_cache_read_price_per_1m`，
--   建在 **models_canonical** 上，不是绑定表。
--   迁移 398 重建 model_offers 视图时 SELECT 了 `cmb.cache_read_price_per_1m`
--   —— 那是**建视图**，它能过说明当时库里已有这些列（来自受追踪链之前的
--   基线 schema），但**没有任何迁移负责把它们带进新库**。
--
--   ⇒ 与 826 依赖 `cmb.success_rate` / `routing_tier` / `billing_mode` 是同一形态，
--     但 842 不继承那个依赖：补列是幂等且零风险的，不补则是硬失败。
--   ⇒ 类型取 numeric(14,6)，与 826 给 baseline 侧定的类型一致。
ALTER TABLE public.credential_model_bindings
    ADD COLUMN IF NOT EXISTS cache_read_price_per_1m  numeric(14,6),
    ADD COLUMN IF NOT EXISTS cache_write_price_per_1m numeric(14,6);

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
    -- ── 842 新增：两个缓存基准价 ────────────────────────────────────────
    -- 放在末尾是因为 CREATE OR REPLACE VIEW 只能追加末尾列。
    -- 加进告警通路后，下面两条**倍率**才有可能被算出来。
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
    END                                             AS cache_write_price_ratio
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
JOIN credentials c      ON c.id = cmb.credential_id
LEFT JOIN providers p   ON p.id = c.provider_id
-- ★ 这个 JOIN 的条件、ORDER BY、LIMIT 1 与 826 **逐字一致**，一字未改。
--   826 的注释记录过一个真实事故：JOIN 写成 OR 时，真库实测会把一条供应商
--   绑定变成三行（canonical_id 与 canonical_raw_name 由不同代码路径写入，
--   不一致是完全可能的状态），同一个供应商价被同时报成「比基准贵 20%」
--   和「比基准便宜 40%」。那个去重修法（名字匹配只在 canonical_id 为空时
--   走 + LATERAL LIMIT 1）**必须原样保留**。
-- ★ 本迁移只往子查询的 SELECT 列表里加两列（见下），
--   加的是 models_canonical 上的列，不影响命中哪一行。
LEFT JOIN LATERAL (
    SELECT mc.id, mc.canonical_name, mc.baseline_price_currency,
           mc.baseline_input_price_per_1m, mc.baseline_output_price_per_1m,
           mc.baseline_price_fetched_at,
           -- 842 新增：这两个列不进子查询，842 的主 SELECT 就引用不到它们。
           mc.baseline_cache_read_price_per_1m, mc.baseline_cache_write_price_per_1m
      FROM public.models_canonical mc
     WHERE mc.id = pm.canonical_id
        OR (pm.canonical_id IS NULL
            AND lower(mc.canonical_name) = lower(pm.canonical_raw_name))
     ORDER BY (pm.canonical_id IS NOT NULL AND mc.id = pm.canonical_id) DESC
     LIMIT 1
) mc ON true;

COMMENT ON VIEW public.v_supplier_price_vs_baseline IS
    'Per (credential, model) supplier price against the vendor baseline. Ratios are NULL unless the currencies match and the baseline is set — a CNY supplier price divided by a USD baseline produces a number that looks like a deviation but is pure noise, so currency_comparable is exposed rather than folded into the ratio. Migration 842 appends the two cache baseline prices and their ratios; they were absent before, which is why cache pricing had no drift coverage at all.';

COMMIT;