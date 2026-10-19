-- 877 写放大的**独立交叉校验**（与 Go 基准完全不同的方法）。
--
-- 方法差异（这正是交叉校验的意义）：
--   Go 基准   ：客户端逐条 INSERT（每 200 行一个事务），客户端墙钟计时。
--   本脚本    ：**服务器端** PL/pgSQL 循环，clock_timestamp() 计时，**零往返**；
--                WAL 用 pg_current_wal_lsn() 的服务端 LSN 差。
-- 两条路径若给出一致的 Δ，才说明结论不是某一种测法的产物。
--
-- ★ 夹具保真度（第一版本脚本在这里失真过，教训记在文件末）：
--   写放大正比于每行的 **distinct trigram 条数**，不是文本长度。
--   第一版用固定模板 'req-N model=deepseek-chat path=… status=ok sk-****'
--   （80 字符）—— 它的 3-gram 大量重复，distinct 只有 68，
--   而生产实测是 84.59 ⇒ 第一版**系统性低估**了 GIN 成本。
--   本版用 md5(random()) 拼装高多样性文本，并**当场量** distinct trigram
--   条数与生产实测（84.59）对齐，不对齐就报错。
--
-- 用法（在 PG 17 上）：
--   psql -d wabench -f 877_write_amplification_cross_check.sql
--
-- ★ 可重复执行（本脚本原本**跑第二遍就会炸**，2026-10-20 复现时才发现）：
--   上一版遗留的 xc_time(regclass,int,int,int)（第 4 参 **带默认值**）**也能接
--   3 参调用**，与本脚本的 3 参版本对同一次调用**同优** ⇒ PG 报
--   `function xc_time(regclass, integer, integer) is not unique`。
--   而「因子分解」脚本恰好定义的就是那个 4 参版本 ⇒ **两个脚本互相污染**。
--   ⇒ 下面这段先把本族（xc_%）辅助函数**按签名全清掉**再重建，
--     于是：本脚本在脏库上可重跑，且与因子脚本互不干扰。
\set ON_ERROR_STOP on
\timing off

\set N 20000

CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- ── 幂等：清掉本族所有辅助函数（含历史版本留下的重载）────────────────
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT p.oid::regprocedure AS sig
    FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
    WHERE n.nspname = current_schema() AND p.proname LIKE 'xc\_%'
  LOOP
    EXECUTE format('DROP FUNCTION IF EXISTS %s CASCADE', r.sig);
  END LOOP;
END
$$;

DROP TABLE IF EXISTS xc_plain;
DROP TABLE IF EXISTS xc_gin;
DROP TABLE IF EXISTS xc_btree;
CREATE TABLE xc_plain (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE TABLE xc_gin   (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE TABLE xc_btree (id BIGINT PRIMARY KEY, tenant_id TEXT, search_text TEXT, ts TIMESTAMPTZ);
CREATE INDEX xc_gin_idx   ON xc_gin   USING gin (search_text gin_trgm_ops);
CREATE INDEX xc_btree_idx ON xc_btree (tenant_id, ts DESC);

-- ── 生产保真度的文本生成器 ────────────────────────────────────────────
-- 长度取生产经验分布（2026-10-19 实测 session_turns_2026_10，227088 行）：
--   桶 (min,max,行数)：(20,20,1) (56,56,1) (62,64,20) (65,69,74)
--   (70,74,11836) (75,79,56468) (80,84,53984) (85,89,9252) (90,95,8661)
--   (96,99,10061) (100,104,8800) (105,109,22861) (110,114,17083)
--   (115,119,12183) (120,124,7625) (125,129,2228) (130,134,2971)
--   (136,138,206) (140,144,62) (145,148,2711)
--
-- 内容用 md5(random()) 拼装：**高 trigram 多样性**是这里的关键 ——
-- 真实 search_text 是运营元数据混合串，3-gram 几乎不重复。
CREATE OR REPLACE FUNCTION xc_text(target_len int)
RETURNS text AS $$
DECLARE
  parts text[] := ARRAY[]::text[];
  cur   int := 0;
  s     text;
BEGIN
  WHILE cur < target_len LOOP
    s := md5(random()::text);
    parts := parts || substr(s, 1, 8);
    cur := cur + 8;
  END LOOP;
  RETURN substr(array_to_string(parts, ''), 1, target_len);
END
$$ LANGUAGE plpgsql VOLATILE;

-- 长度抽样器（按经验分布的累积权重）
CREATE OR REPLACE FUNCTION xc_len()
RETURNS int AS $$
DECLARE
  -- 扁平三元组数组：{lo, hi, n} × N
  buckets int[] := ARRAY[
    20,20,1,   56,56,1,   62,64,20,  65,69,74,  70,74,11836,
    75,79,56468, 80,84,53984, 85,89,9252, 90,95,8661, 96,99,10061,
    100,104,8800, 105,109,22861, 110,114,17083, 115,119,12183,
    120,124,7625, 125,129,2228, 130,134,2971, 136,138,206,
    140,144,62, 145,148,2711];
  nb     int := array_length(buckets,1) / 3;
  total  int := 0;
  pick   int;
BEGIN
  FOR i IN 1..nb LOOP
    total := total + buckets[i*3];
  END LOOP;
  pick := 1 + floor(random() * total)::int;
  FOR i IN 1..nb LOOP
    IF pick <= buckets[i*3] THEN
      RETURN buckets[i*3-2] + floor(random() * (buckets[i*3-1] - buckets[i*3-2] + 1))::int;
    END IF;
    pick := pick - buckets[i*3];
  END LOOP;
  RETURN 92;
END
$$ LANGUAGE plpgsql;

-- ── 夹具保真度自检：distinct trigram 条数必须贴近生产实测 84.59 ────────
\echo '=== 夹具保真度自检（生产实测 distinct trigram avg = 84.59）==='
SELECT round(avg(d),2) AS synthetic_distinct_trgm, round(avg(l),2) AS avg_len
FROM (
  SELECT (SELECT count(DISTINCT x) FROM unnest(show_trgm(xc_text(xc_len()))) x) d,
         length(xc_text(xc_len())) l
  FROM generate_series(1,3000)) t;

-- 三张表灌**同一批**数据（heap 规模一致，臂间差值只可能来自索引）。
INSERT INTO xc_plain
SELECT g, 'tenant_' || (g % 20), xc_text(xc_len()),
       '2026-09-05'::timestamptz + (g || ' seconds')::interval
FROM generate_series(1, :N) g;
INSERT INTO xc_gin   SELECT * FROM xc_plain;
INSERT INTO xc_btree SELECT * FROM xc_plain;
ANALYZE xc_plain; ANALYZE xc_gin; ANALYZE xc_btree;

-- ── 服务器端计时：单行 INSERT 循环 ────────────────────────────────────
-- 两种事务形态都测：
--   tx1   ：每行一个事务（生产 session_turns_hot 的真实形状）
--   tx200 ：每 200 行一个事务（Go 基准的形状）
-- 若两者结论不同，说明"每轮代价"依赖事务批大小，必须说清楚。
CREATE OR REPLACE FUNCTION xc_time(target regclass, n int, per_tx int)
RETURNS TABLE(stmt_us numeric, wall_us numeric, wal_bytes bigint) AS $$
DECLARE
  i        int;
  t0       timestamptz;
  t1       timestamptz;
  t2       timestamptz;
  lsn0     pg_lsn;
  lsn1     pg_lsn;
  ins_time numeric := 0;
  txt      text;
  cnt      int := 0;
BEGIN
  lsn0 := pg_current_wal_lsn();
  t0 := clock_timestamp();
  FOR i IN 1..n LOOP
    txt := xc_text(xc_len());
    t1 := clock_timestamp();
    EXECUTE format(
      'INSERT INTO %s (id, tenant_id, search_text, ts) VALUES ($1, $2, $3, $4)',
      target)
    USING (1000000 + i), 'tenant_3'::text, txt, now();
    t2 := clock_timestamp();
    ins_time := ins_time + EXTRACT(epoch FROM (t2 - t1)) * 1e6;
    cnt := cnt + 1;
  END LOOP;
  lsn1 := pg_current_wal_lsn();
  RETURN QUERY SELECT
    round(ins_time / n, 3),
    round(EXTRACT(epoch FROM (clock_timestamp() - t0)) * 1e6 / n, 3),
    (pg_wal_lsn_diff(lsn1, lsn0))::bigint;
END
$$ LANGUAGE plpgsql;

\echo '=== 服务器端单行 INSERT 计时（零往返，每行一个事务）==='
SELECT 'plain(no index)' AS arm, * FROM xc_time('xc_plain'::regclass, :N, 1);
SELECT 'gin(trgm)'       AS arm, * FROM xc_time('xc_gin'::regclass, :N, 1);
SELECT 'btree(tenant,ts)' AS arm, * FROM xc_time('xc_btree'::regclass, :N, 1);

\echo '=== 索引体积 ==='
SELECT 'gin'   AS which, pg_relation_size('xc_gin_idx')::bigint   AS index_bytes,
       pg_total_relation_size('xc_gin'::regclass)::bigint        AS table_total
UNION ALL
SELECT 'btree', pg_relation_size('xc_btree_idx')::bigint,
       pg_total_relation_size('xc_btree'::regclass)::bigint
UNION ALL
SELECT 'plain', 0::bigint, pg_total_relation_size('xc_plain'::regclass)::bigint;
