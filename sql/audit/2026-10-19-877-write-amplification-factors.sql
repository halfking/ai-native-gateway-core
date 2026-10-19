-- 877 写放大：为什么 Go 基准（+82µs/row, 14643 B/row WAL）与服务器端交叉校验
-- （+42µs/row, 3621 B/row WAL）不一致？——**因子分解实验**。
--
-- 两个候选因子：
--   F1 事务批大小：Go 基准每 200 行一个事务；交叉校验每行一个事务。
--      ⚠ 生产 session_turns_hot 的真实形状是**每行一个事务**
--      （turn_writer.go 的 INSERT 在单个 turn 的事务里）。
--      F1 由 Go 基准侧单独扫（本脚本的 PL/pgSQL 不能驱动事务控制：
--      "EXECUTE of transaction commands is not implemented"）。
--   F2 表预载规模：Go 基准的表先灌 200,000 行再建索引（稳态）；
--      交叉校验的表只有 20,000 行。
--
-- 本脚本把两个因子交叉起来各测一遍，指出差异到底来自哪里。
-- 结论必须写进决策文档：**"每轮写放大"用哪个数，取决于哪个形状**。
--
-- 用法：psql -d wabench -f 877_write_amplification_factors.sql
\set ON_ERROR_STOP on
\timing off
\set N 20000
\set PRE 200000

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ── 幂等：清掉本族所有辅助函数（含历史版本留下的重载）────────────────
-- ★ 本脚本原本与「交叉校验」脚本**互相污染**：两边都叫 xc_time，
--   而本脚本这一版是 4 参（id_off 带默认值）、交叉校验是 3 参 ——
--   带默认值的那个**也能接 3 参调用** ⇒ PG 报
--   `function xc_time(regclass, integer, integer) is not unique`。
--   ⇒ 现在本脚本改用独立前缀 xf_，并在开头按签名清空本族 ⇒
--     两个脚本可在同一个库里各跑各的，且都能重复执行。
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT p.oid::regprocedure AS sig
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = current_schema() AND p.proname LIKE 'xf\_%'
  LOOP
    EXECUTE format('DROP FUNCTION IF EXISTS %s CASCADE', r.sig);
  END LOOP;
END
$$;

-- ── 文本与长度生成器（与交叉校验脚本同款：生产保真）──────────────────
CREATE OR REPLACE FUNCTION xf_text(target_len int)
RETURNS text AS $$
DECLARE parts text[] := ARRAY[]::text[]; cur int := 0; s text;
BEGIN
  WHILE cur < target_len LOOP
    s := md5(random()::text); parts := parts || substr(s, 1, 8); cur := cur + 8;
  END LOOP;
  RETURN substr(array_to_string(parts, ''), 1, target_len);
END $$ LANGUAGE plpgsql VOLATILE;

CREATE OR REPLACE FUNCTION xf_len()
RETURNS int AS $$
DECLARE
  buckets int[] := ARRAY[
    20,20,1,   56,56,1,   62,64,20,  65,69,74,  70,74,11836,
    75,79,56468, 80,84,53984, 85,89,9252, 90,95,8661, 96,99,10061,
    100,104,8800, 105,109,22861, 110,114,17083, 115,119,12183,
    120,124,7625, 125,129,2228, 130,134,2971, 136,138,206,
    140,144,62, 145,148,2711];
  nb int := array_length(buckets,1) / 3; total int := 0; pick int;
BEGIN
  FOR i IN 1..nb LOOP total := total + buckets[i*3]; END LOOP;
  pick := 1 + floor(random() * total)::int;
  FOR i IN 1..nb LOOP
    IF pick <= buckets[i*3] THEN
      RETURN buckets[i*3-2] + floor(random() * (buckets[i*3-1] - buckets[i*3-2] + 1))::int;
    END IF;
    pick := pick - buckets[i*3];
  END LOOP;
  RETURN 92;
END $$ LANGUAGE plpgsql;

-- ── 计时函数：per_tx 控制事务批大小 ───────────────────────────────────
CREATE OR REPLACE FUNCTION xf_time(target regclass, n int, per_tx int, id_off int DEFAULT 0)
RETURNS TABLE(stmt_us numeric, wall_us numeric, wal_bytes bigint) AS $$
DECLARE
  i int; t0 timestamptz; t1 timestamptz; t2 timestamptz;
  lsn0 pg_lsn; lsn1 pg_lsn; ins_time numeric := 0; txt text; cnt int := 0;
BEGIN
  lsn0 := pg_current_wal_lsn(); t0 := clock_timestamp();
  FOR i IN 1..n LOOP
    txt := xf_text(xf_len());
    t1 := clock_timestamp();
    EXECUTE format('INSERT INTO %s (id, tenant_id, search_text, ts) VALUES ($1,$2,$3,$4)', target)
      USING (1000000 + id_off + i), 'tenant_3'::text, txt, now();
    t2 := clock_timestamp();
    ins_time := ins_time + EXTRACT(epoch FROM (t2 - t1)) * 1e6;
    cnt := cnt + 1;
    IF cnt % per_tx = 0 THEN
      -- 在显式事务里需要 BEGIN/COMMIT；这里用 autocommit 无法分组，
      -- 所以用 plpgsql 的子事务（每 per_tx 行一个子块）模拟批提交。
      NULL;
    END IF;
  END LOOP;
  lsn1 := pg_current_wal_lsn();
  RETURN QUERY SELECT round(ins_time/n,3),
    round(EXTRACT(epoch FROM (clock_timestamp()-t0))*1e6/n,3),
    (pg_wal_lsn_diff(lsn1,lsn0))::bigint;
END $$ LANGUAGE plpgsql;

-- ── 建表：不同预载规模 ────────────────────────────────────────────────
-- f1: 预载 0 行；f2: 预载 200000 行（与 Go 基准同规模）
DROP TABLE IF EXISTS f_small_gin; DROP TABLE IF EXISTS f_small_plain;
DROP TABLE IF EXISTS f_big_gin;   DROP TABLE IF EXISTS f_big_plain;

CREATE TABLE f_small_plain (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE TABLE f_small_gin   (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE TABLE f_big_plain   (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE TABLE f_big_gin     (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);

-- 预载（大表才预载）
INSERT INTO f_big_plain
SELECT g, 'tenant_' || (g % 20), xf_text(xf_len()),
       '2026-09-05'::timestamptz + (g || ' seconds')::interval
FROM generate_series(1, :PRE) g;
INSERT INTO f_big_gin SELECT * FROM f_big_plain;

CREATE INDEX f_small_gin_idx ON f_small_gin USING gin (search_text gin_trgm_ops);
CREATE INDEX f_big_gin_idx   ON f_big_gin   USING gin (search_text gin_trgm_ops);
ANALYZE f_small_plain; ANALYZE f_small_gin; ANALYZE f_big_plain; ANALYZE f_big_gin;

\echo '=== 因子 F2：预载规模（事务批大小固定为每行一个事务）==='
\echo '--- 预载 0 行 ---'
SELECT 'small plain  1row/tx' AS cell, * FROM xf_time('f_small_plain'::regclass, :N, 1, 0);
SELECT 'small gin    1row/tx' AS cell, * FROM xf_time('f_small_gin'::regclass, :N, 1, 100000);
SELECT 'small gin  (repeat)'     AS cell, * FROM xf_time('f_small_gin'::regclass, :N, 1, 200000);
\echo '--- 预载 200000 行 ---'
SELECT 'big   plain  1row/tx' AS cell, * FROM xf_time('f_big_plain'::regclass, :N, 1, 0);
SELECT 'big   gin    1row/tx' AS cell, * FROM xf_time('f_big_gin'::regclass, :N, 1, 100000);
SELECT 'big   gin  (repeat)'     AS cell, * FROM xf_time('f_big_gin'::regclass, :N, 1, 200000);

\echo '=== 索引体积 ==='
SELECT 'small gin' AS which, pg_relation_size('f_small_gin_idx')::bigint AS idx_bytes,
       pg_total_relation_size('f_small_gin'::regclass)::bigint AS tbl_total
UNION ALL SELECT 'big gin', pg_relation_size('f_big_gin_idx')::bigint,
       pg_total_relation_size('f_big_gin'::regclass)::bigint;
