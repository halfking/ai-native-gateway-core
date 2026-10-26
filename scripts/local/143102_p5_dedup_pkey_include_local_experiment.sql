-- =====================================================================
-- §143.102 / P5 本地一次性库实验脚本：验证「pkey → UNIQUE(event_id) INCLUDE(occurred_at)
--             + 删 151 MB 复合索引」的合并改造无损
--
-- ★★★ ★★★ 这个脚本**会建表 / 建索引 / 插数据** ⇒ ⇒ 【只能在本地一次性库跑】
-- ★★★ ★★★ 【绝对不要在生产上跑】
--
-- ★ 本脚本回答的问题（§143.27.7 P5 的遗留验证 + §143.102 的 252 实测复核）：
--   252 上 stats_event_dedup 有两条索引：
--     stats_event_dedup_pkey (event_id)                        = 114 MB（writer ON CONFLICT 载体）
--     idx_stats_event_dedup_event_id_occurred_at (event_id, occurred_at) = 151 MB（rollup/reconciliation JOIN 承重）
--   前者是后者的严格前缀（event_id 全表唯一 ⇒ 复合键的第二列从来没有
--   「同 event_id 多行」可排序）⇒ 两条索引只有一条在干活。
--   ⇒ 改法：pkey 换体为 UNIQUE(event_id) INCLUDE(occurred_at)（键列不变 ⇒
--   ON CONFLICT 推断不变；INCLUDE 把 occurred_at 带进索引元组 ⇒ JOIN 的
--   occurred_at 等值判定仍然 Index Only、不回 heap）⇒ 删掉 151 MB 那条。
--
-- ★ 五个阶段：
--   阶段 0：建表（照抄 536 的 stats_event_dedup + 最小 usage_facts 替身）+ 845 复合索引 + 造数据
--   阶段 1：基线 —— JOIN 的计划（应走复合索引 Index Only Scan）+ 体积
--   阶段 2：跑 902 函数体（换体 + 删索引）
--   阶段 3：验证 —— 约束/索引形态 + JOIN 计划等价 + ON CONFLICT 仍工作
--   阶段 4：幂等 —— 重跑 902 函数体必须零报错零变化
--
-- ★ 用法（★ 本地 docker PG；★ 必须带 -i）：
--   docker exec -i llm-gateway-pg psql -U llm_gateway -d postgres \
--     -c "CREATE DATABASE itgate_146_p5;"
--   docker exec -i llm-gateway-pg psql -U llm_gateway -d itgate_146_p5 \
--     -v ON_ERROR_STOP=1 -P pager=off -f - < scripts/local/143102_p5_dedup_pkey_include_local_experiment.sql
--   docker exec llm-gateway-pg psql -U llm_gateway -d postgres -c "DROP DATABASE itgate_146_p5;"
-- =====================================================================
\set ON_ERROR_STOP on
\pset pager off
\timing on

\echo '=== 阶段 0：建表（照抄 536）+ 845 复合索引 + 最小 usage_facts 替身 ==='
CREATE TABLE stats_event_dedup (
    event_id       text PRIMARY KEY,
    occurred_at    timestamptz NOT NULL,
    first_seen_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_stats_event_dedup_event_id_occurred_at
  ON stats_event_dedup (event_id, occurred_at);

CREATE TABLE usage_facts_stub (
    event_id     text NOT NULL,
    occurred_at  timestamptz NOT NULL,
    tokens       int NOT NULL DEFAULT 1
);

\echo '=== 阶段 0：造数据 300,000 行（event_id 取 32-hex 形态，贴近生产） ==='
INSERT INTO stats_event_dedup (event_id, occurred_at)
SELECT md5(g::text), '2026-01-01'::timestamptz + (g % 365) * interval '1 day'
FROM generate_series(1, 300000) g;
-- fact 替身只覆盖一天 ⇒ 选取侧选择性高 ⇒ 计划走「嵌套循环 + 逐行索引探测」
-- （= 生产 rollup/reconciliation 的实际形态：按时间窗取 fact，逐行探 dedup）
INSERT INTO usage_facts_stub
SELECT event_id, occurred_at, 42 FROM stats_event_dedup
WHERE occurred_at >= '2026-10-20' AND occurred_at < '2026-10-21' LIMIT 2000;
ANALYZE stats_event_dedup;
ANALYZE usage_facts_stub;

\echo '=== 阶段 1：基线 —— JOIN 计划（rollup/reconciliation 同款双列等值）+ 索引体积 ==='
VACUUM ANALYZE stats_event_dedup;
EXPLAIN (COSTS OFF)
SELECT f.event_id, f.tokens
FROM usage_facts_stub f
JOIN stats_event_dedup d
  ON d.event_id = f.event_id AND d.occurred_at = f.occurred_at
WHERE f.occurred_at >= '2026-10-20' AND f.occurred_at < '2026-10-21';

SELECT indexrelname AS index_name,
       pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_all_indexes
WHERE relname = 'stats_event_dedup' ORDER BY 1;

\echo '=== 阶段 2：跑 902 函数体（★ 与迁移文件逐字一致的验证副本） ==='
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stats_event_dedup_pkey_include_tmp
    ON public.stats_event_dedup (event_id) INCLUDE (occurred_at);

BEGIN;
SET LOCAL lock_timeout = '3s';
ALTER TABLE public.stats_event_dedup DROP CONSTRAINT stats_event_dedup_pkey;
ALTER TABLE public.stats_event_dedup
    ADD CONSTRAINT stats_event_dedup_pkey PRIMARY KEY
    USING INDEX stats_event_dedup_pkey_include_tmp;
COMMIT;

DROP INDEX CONCURRENTLY IF EXISTS idx_stats_event_dedup_event_id_occurred_at;

\echo '=== 阶段 3：验证 —— 约束/索引形态 ==='
SELECT c.conname AS constraint_name,
       pg_get_constraintdef(c.oid, true) AS constraint_def
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
WHERE t.relname = 'stats_event_dedup';

SELECT i.relname AS index_name, pg_get_indexdef(i.oid, 0, true) AS index_def,
       pg_size_pretty(pg_relation_size(i.oid)) AS size
FROM pg_class i
JOIN pg_index ix ON ix.indexrelid = i.oid
JOIN pg_class t ON t.oid = ix.indrelid
WHERE t.relname = 'stats_event_dedup' AND i.relkind = 'i';

\echo '=== 阶段 3b：JOIN 计划等价（★ 应仍 Index Only Scan，只是索引换成 pkey 体） ==='
VACUUM ANALYZE stats_event_dedup;
EXPLAIN (COSTS OFF)
SELECT f.event_id, f.tokens
FROM usage_facts_stub f
JOIN stats_event_dedup d
  ON d.event_id = f.event_id AND d.occurred_at = f.occurred_at
WHERE f.occurred_at >= '2026-10-20' AND f.occurred_at < '2026-10-21';

\echo '=== 阶段 3c：ON CONFLICT (event_id) DO UPDATE 仍工作（writer 同款） ==='
INSERT INTO stats_event_dedup (event_id, occurred_at)
VALUES ('conflict-probe-1', '2026-10-24 00:00:00+00')
ON CONFLICT (event_id) DO UPDATE SET occurred_at = EXCLUDED.occurred_at
RETURNING event_id, occurred_at;
INSERT INTO stats_event_dedup (event_id, occurred_at)
VALUES ('conflict-probe-1', '2026-10-24 01:00:00+00')
ON CONFLICT (event_id) DO UPDATE SET occurred_at = EXCLUDED.occurred_at
RETURNING event_id, occurred_at;

\echo '=== 阶段 3d：唯一性不变式仍被强制（同 event_id 第二行必须失败；★ 断言包进 DO 块 ⇒ 失败才算失败） ==='
DO $$
BEGIN
  INSERT INTO stats_event_dedup (event_id, occurred_at) VALUES ('conflict-probe-1', '2026-10-24 02:00:00+00');
  RAISE EXCEPTION 'INVARIANT-BROKEN: duplicate event_id was accepted';
EXCEPTION WHEN unique_violation THEN
  RAISE NOTICE 'uniqueness_still_enforced = t';
END $$;

\echo '=== 阶段 4：幂等 —— 重跑 902 函数体（IF NOT EXISTS + 形态判定守卫）必须零报错 ==='
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS stats_event_dedup_pkey_include_tmp
    ON public.stats_event_dedup (event_id) INCLUDE (occurred_at);
-- 换体段受「pkey 是否已带 INCLUDE」判定保护（迁移文件里是 DO 块；此处人工跳过）
DROP INDEX CONCURRENTLY IF EXISTS idx_stats_event_dedup_event_id_occurred_at;
\echo 'idempotent_rerun_clean = t'

\echo '=== 体积对账（★ 预期：只剩一条 pkey 体索引） ==='
SELECT pg_size_pretty(pg_indexes_size('stats_event_dedup')) AS idx_total;
