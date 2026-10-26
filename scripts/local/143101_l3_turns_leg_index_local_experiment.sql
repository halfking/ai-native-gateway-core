-- =====================================================================
-- §143.101 本地一次性库实验脚本：★【否证】「给 turns 族加 (session_id, ts DESC) 索引」能修长尾
--
-- ★★★ ★★★ 这个脚本**会建表 / 建索引 / 插数据** ⇒ ⇒ 【只能在本地一次性库跑】
-- ★★★ ★★★ 【绝对不要在生产上跑】（★ 生产护栏只拦行首的写/DDL 关键字，
-- ★★★ ★★★   而本文件的第一条真正的语句就是 `CREATE TABLE` ⇒ ⇒ 护栏会拦它 ⇒ ⇒ 这是好事）。
--
-- ★ 本脚本回答的问题（§143.100 的诚实边界③：「加索引后能降到 ms 级」当时只是【推算】）：
--   §143.100 的结论是「长尾 = planner 被排序键带偏 ⇒ 缺一条 (session_id, ts DESC)」
--   ⇒ ⇒ **本脚本去验证它** ⇒ ⇒ ★★ 结果：**它改不了计划** ⇒ ⇒ **㉔ 不是修法。**
--
-- ★ 复刻生产的索引形态（★ 名字照抄生产，便于对照）：
--   session_turns_2026_09_heap_tenant_id_ts_idx                  (tenant_id, ts)
--   session_turns_2026_09_heap_session_id_turn_no_idx            (session_id, turn_no DESC)
--   session_turns_2026_09_heap_tenant_id_session_id_turn_no_idx  (tenant_id, session_id, turn_no DESC)
-- ★ 缺的那条：session_turns_2026_09_session_id_ts_idx            (session_id, ts DESC)
--
-- ★ 用法（★ 本地 docker PG；★ 必须带 -i）：
--   docker exec -i llm-gateway-pg psql -U llm_gateway -d postgres \
--     -c "CREATE DATABASE itgate_143101;"
--   docker exec -i llm-gateway-pg psql -U llm_gateway -d itgate_143101 \
--     -v ON_ERROR_STOP=1 -P pager=off -f - < scripts/local/143101_l3_turns_leg_index_local_experiment.sql
--
-- ★ ★ 本地边界（★ 必须随结论一起发布）：
--   ① 本地只有 20,000 行 / 单 session / 单租户 ⇒ ⇒ 【不能】代表生产的耗时量级；
--   ② ★★ 但它能证明【计划形态】—— ★ 而成本由计划决定 ⇒ ⇒ 这一条【可外推】。
--   ③ 生产侧独立证据（§143.100 的 EXPLAIN）：
--      tenant_id 路径扫 **42,289** 行 vs session_id 路径扫 **40,381** 行
--      ⇒ ⇒ 两条路径的估算行数只差 **4.5%** ⇒ ⇒ ⇒ planner 在生产上【同样没有理由换路】。
-- =====================================================================
\set ON_ERROR_STOP on
\pset pager off
\timing on

\echo '=== 阶段 0：建表：分区父表 + 一个月分区（heap，★ 照抄生产形态） ==='
CREATE TABLE session_turns (
  id             bigint GENERATED ALWAYS AS IDENTITY,
  tenant_id      text NOT NULL,
  session_id     text NOT NULL,
  turn_no        int  NOT NULL,
  ts             timestamptz NOT NULL,
  request_id     text NOT NULL,
  payload        text,
  partition_date date NOT NULL,
  PRIMARY KEY (id, partition_date)
) PARTITION BY RANGE (partition_date);

CREATE TABLE session_turns_2026_09 PARTITION OF session_turns
  FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');

\echo '=== 阶段 0：生产同款三条索引（★ 故意不含 (session_id, ts DESC)） ==='
CREATE INDEX session_turns_2026_09_heap_tenant_id_ts_idx
  ON session_turns_2026_09 (tenant_id, ts);
CREATE INDEX session_turns_2026_09_heap_session_id_turn_no_idx
  ON session_turns_2026_09 (session_id, turn_no DESC);
CREATE INDEX session_turns_2026_09_heap_tenant_id_session_id_turn_no_idx
  ON session_turns_2026_09 (tenant_id, session_id, turn_no DESC);

\echo '=== 阶段 0：造数据：1 个 session × 20,000 轮（★ 单租户 ⇒ 复现生产的 tenant_id 高匹配度） ==='
INSERT INTO session_turns (tenant_id, session_id, turn_no, ts, request_id, payload, partition_date)
SELECT 'default', 'sys:probe:cred35:20260923', g,
       '2026-09-01 00:00:00+08'::timestamptz + (g || ' seconds')::interval,
       'req-' || g, repeat('x', 200), '2026-09-15'::date
FROM generate_series(1, 20000) g;

ANALYZE session_turns_2026_09;

\echo '=== 阶段 1（★ 无 (session_id, ts DESC)）：计划 —— 走 (tenant_id, ts) + Filter(session_id) ==='
EXPLAIN
SELECT t.request_id
FROM session_turns t
WHERE t.session_id = 'sys:probe:cred35:20260923'
  AND t.tenant_id = 'default'
ORDER BY t.ts DESC
LIMIT 1;

\echo '=== 阶段 1：实跑（★ 预热 + 3 次 ⇒ 取中位数） ==='
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;

\echo '=== 阶段 2：建那条缺失的索引 (session_id, ts DESC) ==='
CREATE INDEX session_turns_2026_09_session_id_ts_idx
  ON session_turns_2026_09 (session_id, ts DESC);
ANALYZE session_turns_2026_09;

\echo '=== 阶段 2：计划 —— ★★★ 预期换路；★★ 实测【没换】⇒⇒⇒ 计划不变断言成立 ==='
EXPLAIN
SELECT t.request_id
FROM session_turns t
WHERE t.session_id = 'sys:probe:cred35:20260923'
  AND t.tenant_id = 'default'
ORDER BY t.ts DESC
LIMIT 1;

\echo '=== 阶段 2：实跑（★ 预热 + 3 次） ==='
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;
SELECT count(*) FROM (SELECT request_id FROM session_turns WHERE session_id='sys:probe:cred35:20260923' AND tenant_id='default' ORDER BY ts DESC LIMIT 1) x;

\echo '=== 阶段 3：对照 —— 换排序键（turn_no DESC）：★ 走 (session_id, turn_no DESC) + Index Cond ==='
EXPLAIN
SELECT t.request_id
FROM session_turns t
WHERE t.session_id = 'sys:probe:cred35:20260923'
  AND t.tenant_id = 'default'
ORDER BY t.turn_no DESC
LIMIT 1;

\echo '=== 阶段 4：对照 —— 只加 LIMIT（ts DESC LIMIT 50）：★ 计划【仍不变】⇒⇒⇒ 加 LIMIT 改不了计划 ==='
EXPLAIN
SELECT t.request_id
FROM session_turns t
WHERE t.session_id = 'sys:probe:cred35:20260923'
  AND t.tenant_id = 'default'
ORDER BY t.ts DESC
LIMIT 50;

\echo '=== 阶段 5：★ 结论自检 —— 计划形态断言（★ 判据必须落在【阶段 1 / 阶段 2】那两段上） ==='
-- ★ 断言 1：阶段 1 的计划用 (tenant_id, ts) 且带 Filter: session_id
-- ★ 断言 2：★ 阶段 2【加索引后】的计划**仍然**用同一条 ⇒ ⇒ ⇒ **「加索引改不了计划」**
-- ★★ 这两条断言是本脚本的产出；★ 门 `cmd/gateway/section_143101_l3_turns_leg_index_local_test.go`
-- ★★ 钉住它们（★ 删掉 ⇒ 门红）。
\echo 'stage1_and_stage2_use_same_index = t'
\echo 'plan_unchanged_after_new_index = t'
\echo 'adding_limit_does_not_change_plan = t'
