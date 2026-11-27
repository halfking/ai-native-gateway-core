-- Migration: 999c_tool_usage_stats_columns
-- Purpose: 给 tool_usage_stats / tool_usage_stats_hot 补 134 形状的统计列 ——
--          让 domains/toolexecution 的工具统计链（写侧与读侧）真的能落库与读出
-- Phase: 第 31 轮（存量缺口收口轮 · 2026-11-26）
-- Author: huangxt
-- Date: 2026-11-26
--
-- ★ 背景：★ 「生产 SQL 引用的列必须有投递来源创建」门（internal/schemadelivery，
--   2026-11-26 第 30 轮新立）第一次校准就抓到了这条链：domains/toolexecution 的
--   postgres_store.go **写侧**（INSERT INTO tool_usage_stats_hot）与**读侧**
--   （SELECT … FROM tool_usage_stats / tool_usage_stats_hot）用的都是
--   sql/migrations/domain/134_tool_execution.sql 那套列（tool_name / date /
--   total_calls / p50…），而：
--   ★ 134 **不在任何投递通道里**（deploy=0、installer=0）；
--   ★ 仓里的**权威形状**是另一套 —— 335_partition_tool_usage_stats.sql 与
--     348_tool_usage_stats_hot_independence.sql 把它定成 tool_id / tenant_id /
--     usage_date / call_count / success_count / error_count 那一套
--     （基线 01-schema.sql 与之相同）。
--   ⇒ 生产库上这两张表是 **tool_id 形状** ⇒ 代码的统计 INSERT 与 SELECT 全部
--     42703 ⇒ 工具统计链（每次工具调用的分位数、Top 用户、失败率）**在生产上
--     从未落过数据**。
--
-- ★ 为什么不是「把 134 搬进通道」就完事（★ 核对后的结论，owner 2026-11-26 决定）：
--   写侧的 INSERT **不提供** tool_id / tenant_id / usage_date，而目标表上这三列
--   **都是 NOT NULL** ⇒ 只加 134 那 13 列，INSERT 仍会因 NOT NULL 违反而失败
--   ⇒ 必须同时把这三列改成可空。
--
-- ★ 两套键并存的后果（如实说明，不掩饰）：
--   库里此后同时存在两套统计键：
--     ★ tool_id 形状 ＝ 335/348 定的旧口径（call_count / success_count / error_count）；
--     ★ tool_name 形状 ＝ 本迁移补的 134 口径（total_calls / success_calls /
--       failed_calls / timeout_calls / p50…）。
--   本迁移**不删**旧列、不动旧数据 ⇒ 旧口径的历史数据仍在，可供既有读面使用；
--   新口径由 domains/toolexecution 的 UPSERT 独占写入。
--   ★ tool_name 的取值来源是**同一仓内**的执行明细表 session_tools.tool_name
--     （706_session_family_s1a.sql 建、在通道里）⇒ 两套键指向同一批工具，可对账。
--
-- ★ ★★ 唯一约束只建在 hot 表上（★ 这是本件最关键的一处设计，理由三条）：
--   ① 写侧的 UPSERT 目标是 **tool_usage_stats_hot**（不是父表）
--      ⇒ ON CONFLICT (tool_name, date) 需要落在哪张表，约束就得建在哪张表；
--   ② 父表 tool_usage_stats 是**分区表**（基线：PARTITION BY RANGE (created_at)；
--      生产上 335 那份件建的可能是 RANGE (usage_date)）⇒ PostgreSQL **要求分区表
--      上的唯一约束必须包含全部分区键** ⇒ 建 UNIQUE (tool_name, date) 在父表上
--      会直接报错（unique constraint on partitioned table must include all
--      partitioning columns）⇒ ⇒ **约束只能建在非分区表 hot 上**；
--   ③ hot 表由 348_tool_usage_stats_hot_independence.sql 建为
--      **独立表**（LIKE tool_usage_stats INCLUDING ALL，不是分区表）⇒ 约束可建。
--   ⇒ 读侧（父表 ∪ hot 的 UNION ALL）不依赖任何唯一约束 ⇒ 父表不加约束不影响读。
--
-- ★ 幂等：ADD COLUMN IF NOT EXISTS / DROP NOT EXISTS 皆幂等；
--   唯一约束用「先查 pg_constraint 再建」的形式（PG 没有 ADD CONSTRAINT IF NOT EXISTS）。

\set ON_ERROR_STOP on

SET lock_timeout = '5s';
SET statement_timeout = '10min';

BEGIN;

-- ── 1. tool_usage_stats：加 134 形状的 13 列 ────────────────────────────────
ALTER TABLE public.tool_usage_stats
    ADD COLUMN IF NOT EXISTS tool_name       VARCHAR(128),
    ADD COLUMN IF NOT EXISTS date            DATE,
    ADD COLUMN IF NOT EXISTS total_calls     BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS success_calls   BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failed_calls    BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS timeout_calls   BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS avg_duration_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS p50_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS p95_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS p99_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS unique_users    INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS unique_sessions INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS top_users       JSONB DEFAULT '[]'::jsonb;

-- ── 2. tool_usage_stats：把代码不提供的三个 NOT NULL 列改成可空 ───────────────
-- ★ tool_id / tenant_id 是旧口径的主键成分与租户隔离列；usage_date 是旧口径的
--   统计日与（部分库上的）分区键 ⇒ 改成可空**不删数据**，只是允许「按 tool_name
--   口径写入的行」没有旧口径的键。
-- ★ ★ usage_date 也要去 NOT NULL：★ 代码的 INSERT 给的是 `date`（134 形状），
--   不给 usage_date ⇒ 不去就会因 NOT NULL 违反而失败。
ALTER TABLE public.tool_usage_stats ALTER COLUMN tool_id    DROP NOT NULL;
ALTER TABLE public.tool_usage_stats ALTER COLUMN tenant_id  DROP NOT NULL;
ALTER TABLE public.tool_usage_stats ALTER COLUMN usage_date DROP NOT NULL;

-- ── 3. tool_usage_stats：给旧行补上与 usage_date 一致的 date ─────────────────
-- ★ 旧行（tool_id 口径）没有 date ⇒ 补上（tool_name 留 NULL，NULL 不参与唯一
--   约束的相等比较 ⇒ 不与新口径行冲突）。
UPDATE public.tool_usage_stats
   SET date = usage_date
 WHERE date IS NULL AND usage_date IS NOT NULL;

-- ── 4. tool_usage_stats_hot：同一套 13 列 ＋ 同样三列去 NOT NULL ─────────────
ALTER TABLE public.tool_usage_stats_hot
    ADD COLUMN IF NOT EXISTS tool_name       VARCHAR(128),
    ADD COLUMN IF NOT EXISTS date            DATE,
    ADD COLUMN IF NOT EXISTS total_calls     BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS success_calls   BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS failed_calls    BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS timeout_calls   BIGINT DEFAULT 0,
    ADD COLUMN IF NOT EXISTS avg_duration_ms DOUBLE PRECISION,
    ADD COLUMN IF NOT EXISTS p50_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS p95_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS p99_duration_ms BIGINT,
    ADD COLUMN IF NOT EXISTS unique_users    INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS unique_sessions INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS top_users       JSONB DEFAULT '[]'::jsonb;

ALTER TABLE public.tool_usage_stats_hot ALTER COLUMN tool_id    DROP NOT NULL;
ALTER TABLE public.tool_usage_stats_hot ALTER COLUMN tenant_id  DROP NOT NULL;
ALTER TABLE public.tool_usage_stats_hot ALTER COLUMN usage_date DROP NOT NULL;

UPDATE public.tool_usage_stats_hot
   SET date = usage_date
 WHERE date IS NULL AND usage_date IS NOT NULL;

-- ── 5. hot 表的唯一约束（供 ON CONFLICT (tool_name, date) 用）────────────────
-- ★ 只建在 hot 表（★ 理由见文件头第 ★ ★★ 段：父表是分区表，PG 不允许）。
-- ★ PG 没有 ADD CONSTRAINT IF NOT EXISTS ⇒ 先查 pg_constraint 再建。
-- ★ 判重：同一 (tool_name, date) 若已有多行 ⇒ 建约束会失败 ⇒ 先把重复行合并
--   （保留 id 最小的一行，其余行删掉）—— 旧口径的行 tool_name 为 NULL，不参与。
DO $$
DECLARE
    dupe RECORD;
BEGIN
    FOR dupe IN
        SELECT tool_name, date, count(*) AS n
          FROM public.tool_usage_stats_hot
         WHERE tool_name IS NOT NULL AND date IS NOT NULL
         GROUP BY tool_name, date
        HAVING count(*) > 1
    LOOP
        RAISE NOTICE '999c: 合并 hot 表重复行 (tool_name=%, date=%, n=%)', dupe.tool_name, dupe.date, dupe.n;
        DELETE FROM public.tool_usage_stats_hot t
         USING public.tool_usage_stats_hot keep
         WHERE t.tool_name = dupe.tool_name
           AND t.date = dupe.date
           AND t.id > keep.id
           AND keep.tool_name = dupe.tool_name
           AND keep.date = dupe.date;
    END LOOP;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'public.tool_usage_stats_hot'::regclass
           AND contype = 'u'
           AND conname = 'tool_usage_stats_hot_tool_name_date_key'
    ) THEN
        ALTER TABLE public.tool_usage_stats_hot
            ADD CONSTRAINT tool_usage_stats_hot_tool_name_date_key UNIQUE (tool_name, date);
    END IF;
END $$;

-- ── 6. 分区子表：★ 无需（也不能）单独处理 ★ ─────────────────────────────────
-- ★ ★★ 2026-11-26 真库实测改正（★ 这个坑只有真库能踩出来）：
--   PG **不允许**直接对分区子表做 ALTER TABLE … ADD COLUMN
--     ⇒ ERROR: cannot add column to a partition
--   ★ 而**父表加列本来就会自动传播到全部分区子表**（PG 的分区机制）⇒
--     ⇒ 第 1 段的 ALTER TABLE public.tool_usage_stats ADD COLUMN …
--        一句就把 13 列带给每个子表 ⇒ **本段是多余的，且执行会失败** ⇒ 删掉。
--   ★ 同理，第 2 段的 DROP NOT NULL（改父表）也会自动传播到子表 ⇒
--     不对子表单独做（★ 显式对子表 ALTER COLUMN 会与分区机制冲突）。
--   ★ 保留这段注释，是为了让下一个读的人知道「为什么这里曾经有一段遍历 pg_inherits
--     的 DO 块，而它被删了」—— 免得有人按「父表加列不传播」的旧认知再写回来。

COMMENT ON COLUMN public.tool_usage_stats.tool_name IS
    '工具统计口径 B（134 形状）：工具名；与 tool_id 口径并存，2026-11-26 随 999c 投递';
COMMENT ON COLUMN public.tool_usage_stats.date IS
    '工具统计口径 B（134 形状）：统计日；与 usage_date 口径并存';


-- ★ 运行时台账自登记（双账本纪律：≥952 起迁移文件必须自带INSERT INTO public.schema_migrations；999b/c/d 同型补登记 2026-11-28）。
--   否则运行时台账无行（runner 的 recordAppliedVersion 只认数字前缀，字母后缀件解析不出版本）⇒ 台账断代；down 件的 DELETE 已在库、up 侧此前缺失。
INSERT INTO public.schema_migrations (version, description)
VALUES ('999c', 'tool usage stats columns (stats shape B)')
ON CONFLICT (version) DO NOTHING;

COMMIT;
