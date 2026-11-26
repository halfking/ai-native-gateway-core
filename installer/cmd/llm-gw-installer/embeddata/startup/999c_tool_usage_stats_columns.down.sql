-- Migration: 999c_tool_usage_stats_columns (down)
-- Purpose: 回滚 999c —— 删掉 tool_usage_stats / tool_usage_stats_hot 的 134 形状那 13 列
--          ＋ 恢复 tool_id / tenant_id / usage_date 的 NOT NULL ＋ 删掉 hot 表那个唯一约束
--
-- ★ 回滚的边界（如实说明）：
--   ① 本迁移**只删列**，不删数据 —— 但删列会**丢掉**新口径（tool_name 形状）写入的
--      统计值（total_calls / p50… / top_users）。⇒ 回滚前若要保留这些数，先导出。
--   ② 恢复 NOT NULL 前，必须先把 tool_id / tenant_id / usage_date 为 NULL 的行处理掉
--      （新口径写入的行没有这三个值）⇒ 下面先把它们补成占位值，
--      否则 ALTER COLUMN ... SET NOT NULL 会失败。
--   ③ 回滚后 domains/toolexecution 的统计链会重新变成 42703（代码仍用 134 形状）
--      ⇒ 回滚必须与代码回滚同批。
--   ④ ★ 唯一约束只建在 hot 表上（★ up 件里写明理由：父表是分区表，PG 不允许
--      在分区表上建不含分区键的唯一约束）⇒ down 件也只删 hot 表那一个。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ── 1. 先处理三个可为空的列（否则 SET NOT NULL 会失败）──────────────────────
-- ★ tool_id 可取同表的 tool_name（134 形状的工具名，而 335/348 口径的 tool_id
--   也是工具标识 ⇒ 语义同源）；tenant_id 无来源 ⇒ 填 'default'（与 389 那份件
--   对平台级记录的处理一致）；usage_date 可取同表的 date。
UPDATE public.tool_usage_stats
   SET tool_id = tool_name
 WHERE tool_id IS NULL AND tool_name IS NOT NULL;

UPDATE public.tool_usage_stats
   SET tenant_id = 'default'
 WHERE tenant_id IS NULL;

UPDATE public.tool_usage_stats
   SET usage_date = date
 WHERE usage_date IS NULL AND date IS NOT NULL;

UPDATE public.tool_usage_stats
   SET usage_date = CURRENT_DATE
 WHERE usage_date IS NULL;

UPDATE public.tool_usage_stats_hot
   SET tool_id = tool_name
 WHERE tool_id IS NULL AND tool_name IS NOT NULL;

UPDATE public.tool_usage_stats_hot
   SET tenant_id = 'default'
 WHERE tenant_id IS NULL;

UPDATE public.tool_usage_stats_hot
   SET usage_date = date
 WHERE usage_date IS NULL AND date IS NOT NULL;

UPDATE public.tool_usage_stats_hot
   SET usage_date = CURRENT_DATE
 WHERE usage_date IS NULL;

-- ── 2. 删 hot 表的唯一约束（父表上没有，不存在要删的）────────────────────────
ALTER TABLE public.tool_usage_stats_hot DROP CONSTRAINT IF EXISTS tool_usage_stats_hot_tool_name_date_key;

-- ── 3. 恢复 NOT NULL ────────────────────────────────────────────────────────
-- ★ ★ 只对**父表**做：★ 分区子表的列是继承属性 ⇒ 改父表即生效；
--   ★ 显式对子表 ALTER COLUMN SET NOT NULL 会与分区机制冲突 ⇒ 不做。
ALTER TABLE public.tool_usage_stats      ALTER COLUMN tool_id    SET NOT NULL;
ALTER TABLE public.tool_usage_stats      ALTER COLUMN tenant_id  SET NOT NULL;
ALTER TABLE public.tool_usage_stats      ALTER COLUMN usage_date SET NOT NULL;
ALTER TABLE public.tool_usage_stats_hot  ALTER COLUMN tool_id    SET NOT NULL;
ALTER TABLE public.tool_usage_stats_hot  ALTER COLUMN tenant_id  SET NOT NULL;
ALTER TABLE public.tool_usage_stats_hot  ALTER COLUMN usage_date SET NOT NULL;

-- ── 4. 删列（只删父表；子表随父表自动跟随）────────────────────────────
-- ★ ★★ 2026-11-26 真库实测改正：★ PG **不允许**直接对分区子表做
--   ALTER TABLE … DROP COLUMN（★ ERROR: cannot drop column from a partition）
--   ⇒ 父表删列时 PG 会自动从所有分区子表删掉同一列 ⇒ **只删父表即可**。
--   ★ 上一版这里有一段遍历 pg_inherits 的 DO 块去逐个删子表 ⇒ 执行会失败 ⇒ 删掉。
--   ★ tool_usage_stats_hot **不是**分区表（348 建为独立表）⇒ 它自己删。
ALTER TABLE public.tool_usage_stats
    DROP COLUMN IF EXISTS tool_name,
    DROP COLUMN IF EXISTS date,
    DROP COLUMN IF EXISTS total_calls,
    DROP COLUMN IF EXISTS success_calls,
    DROP COLUMN IF EXISTS failed_calls,
    DROP COLUMN IF EXISTS timeout_calls,
    DROP COLUMN IF EXISTS avg_duration_ms,
    DROP COLUMN IF EXISTS p50_duration_ms,
    DROP COLUMN IF EXISTS p95_duration_ms,
    DROP COLUMN IF EXISTS p99_duration_ms,
    DROP COLUMN IF EXISTS unique_users,
    DROP COLUMN IF EXISTS unique_sessions,
    DROP COLUMN IF EXISTS top_users;

ALTER TABLE public.tool_usage_stats_hot
    DROP COLUMN IF EXISTS tool_name,
    DROP COLUMN IF EXISTS date,
    DROP COLUMN IF EXISTS total_calls,
    DROP COLUMN IF EXISTS success_calls,
    DROP COLUMN IF EXISTS failed_calls,
    DROP COLUMN IF EXISTS timeout_calls,
    DROP COLUMN IF EXISTS avg_duration_ms,
    DROP COLUMN IF EXISTS p50_duration_ms,
    DROP COLUMN IF EXISTS p95_duration_ms,
    DROP COLUMN IF EXISTS p99_duration_ms,
    DROP COLUMN IF EXISTS unique_users,
    DROP COLUMN IF EXISTS unique_sessions,
    DROP COLUMN IF EXISTS top_users;

DELETE FROM public.schema_migrations WHERE version = '999c';

COMMIT;
