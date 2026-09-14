-- Migration 711: session_turns.cost_usd numeric(12,6) → numeric(14,8).
-- 存储优化方案 v2 E5（2026-09-15 观察台账 Round 1b 首个真实 G1-cost 样本）：
-- turns 列 12,6 在双写期对 v1 request_logs.cost_usd numeric(14,8) 舍入
-- （0.00001870 → 0.000019），D7 计费等值对账出现恒定 1e-8~5e-7 量级漂移。
-- 列放宽到与 v1 同精度 14,8 后，双写期两侧可精确判等。
--
-- cost_display 不动：该列为 DOUBLE PRECISION（53 位尾数 ≈15-16 位十进制
-- 有效数字），无 numeric scale 舍入问题，E5 样本不涉及。
--
-- 回填（把双写期已有 turns 行的 cost_usd 从 v1 终态行拷回 8 位精度值）
-- 不在迁移内执行：数据修补与 DDL 分离，按 plan §4 惯例走独立脚本
-- scripts/audit/turns_cost_precision_backfill.sql，应用侧核对差异行数后执行。
--
-- 分区表：父表 ALTER 自动级联全部分区（2026_07..2026_10 + default，
-- 本机实测 26.8 万行，重写秒级）。放大 precision/scale 为纯放宽，无值截断。

ALTER TABLE public.session_turns
    ALTER COLUMN cost_usd TYPE numeric(14, 8);
