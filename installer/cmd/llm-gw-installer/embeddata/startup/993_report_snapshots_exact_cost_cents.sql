-- 993_report_snapshots_exact_cost_cents.sql
-- F4 落实（2026-11-20 看板对账口径对齐轮；docs/audit/2026-11-20-看板对账口径对齐-主档.md §五 L-cost）。
--
-- ## 为什么改
--
-- estimated_cost_cents 此前是 bigint，写面按**分组**取整
-- （每个 grain/provider/tenant 桶各自 ROUND(SUM(cost_amount)*100)），
-- 读面的「区间总计」是各分组行求和 ⇒
--
--     Σ round(分组) ≠ round(Σ 分组)          —— 每天差 ≤3¢（211 实测定量）
--
-- 而看板英雄区（本轮起读 usage_facts 真源）是**一次求和**的精确值 ⇒
-- 对账页与看板的总费用永远差着这几分钱。
--
-- ## 改法（与 2026-11-17 对账口径复审轮 F4 的立项方案一致）
--
--   ① 本迁移：列精度 bigint → numeric(24,6)（分为单位的精确小数；
--     cost_amount 是 numeric(20,8)，×100 后 6 位小数完全无损）；
--   ② 写面（reportrollup 六段聚合 SQL）去掉逐分组 ROUND，存精确分值；
--   ③ 读面总计 = Σ 精确分值 = 精确；展示在渲染时一次取整（fmtUsd 的
--     toFixed(2)）⇒ 与看板 round(SUM, 2) 逐字相等；
--   ④ 历史快照回填：对 usage_facts 仍覆盖的日期重跑 RollupDay
--     （幂等 upsert，从真源重算精确分值）；更早的日期保留旧取整值
--     （真源已不在，无法重算——登记于主档）。
--
-- ## 兼容性
--
--   · 表很小（单日 ~1k 行 × 历史天数），ALTER TYPE 即时完成；
--   · DEFAULT 0 / NOT NULL 语义不变；
--   · Go 读面（reportrollup）同步改为 float64 扫描，JSON 键与语义不变
--     （值从整数分变为精确小数分，前端 /100 + toFixed(2) 无需改动）。
BEGIN;

ALTER TABLE public.report_snapshots
    ALTER COLUMN estimated_cost_cents TYPE numeric(24,6);

COMMIT;

-- ============================================================================
-- 自登记（≥952 双账本纪律，960 立门）
-- ★ 2026-11-20 会话分析闭环审计轮合并补登记：上列车漏写自登记行 ⇒ 211 上
--   DDL 已生效（estimated_cost_cents 实测 numeric）而 schema_migrations 无行，
--   下次启动会被 runner 判为 pending 重放（本迁移重放安全：ALTER TYPE
--   numeric→numeric 即时、表 ~1k 行/日）；补行让重放同时落账，154/245 首投
--   即走正规记账。
-- ============================================================================
INSERT INTO public.schema_migrations (version, description)
VALUES ('993', 'report_snapshots_exact_cost_cents: estimated_cost_cents bigint → numeric(24,6)，写面去 ROUND 存精确分值（对账总费用与看板逐字相等）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
