-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），
-- installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go
-- noTransactionMarker，877/888 同款）。无该标记时全新安装会失败于
-- "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
--
-- Migration 898: 补上 public.session_bodies_hot 的 ts 首列索引。
--
-- ═══════════════════════════════════════════════════════════════════════
-- 为什么需要它（★ 全部为 2026-10-23 生产只读实测，结论可复算）
-- ═══════════════════════════════════════════════════════════════════════
--
-- 【症状】154 网关日志（2026-10-23）：promote_session_bodies_hot_to_partition
--   多数 tick 成功（rows=511/589/1673/737/520/446/558/464/511…），**偶发**
--   `timeout: context deadline exceeded`（06:45:15.771 / 10:52:33.434 /
--   11:55:48）。⇒ 不是 23505 整批回滚，是批次超时。
--   ★ 时间轴闭环：06:43:14 candidate_failure_logs → 06:44:14 session_turns_hot
--   FAILED（跑满 60s）→ 06:44:15 session_turn_details_hot（秒级）→
--   06:45:15 session_bodies_hot FAILED（又跑满 60s）→ 06:45:23
--   supplier_errors_hot 仍成功 ⇒ ⇒ 周期预算未耗尽 ⇒ 那 60s 是本表自己的
--   批次预算，不是级联。
--
-- 【根因 A：批次选取无索引承重】
--   session_bodies_hot 的 6 个索引中**没有一个以 ts 为首列**
--   （idx_session_bodies_hot_request / idx_session_bodies_hot_tenant_session_
--   turn_request / session_bodies_hot_pkey / session_bodies_hot_unique /
--   session_bodies_with_current_month / uq_session_bodies_hot_final_full）。
--   对照组 session_turns_hot **有** idx_session_turns_hot_ts (ts, id,
--   partition_date) ⇒ 规划器实读：
--     session_bodies_hot = Seq Scan + Sort（cost 1602~1623）
--     session_turns_hot  = Index Only Scan using idx_session_turns_hot_ts
--                          （cost 219~385）
--   ⇒ 同一张 hot 表的批次选取，一个走索引、一个全表扫 + 排序。
--
-- 【★ 但选取成本不是 60s 的主因 —— 本轮自我修正】
--   2026-10-23 实测（252 真库，EXPLAIN ANALYZE）：
--     Limit (actual time=26.955..29.244 rows=4770)
--       -> Sort (actual time=26.952..28.419 rows=4770)
--            Sort Method: quicksort  Memory: 4198kB     ← **未溢写**
--            -> Seq Scan on session_bodies_hot (actual time=0.021..13.932)
--   ⇒ 批次选取只花 **30ms**，且 Sort 只用 4198kB（work_mem=8MB）⇒ **不溢写**。
--   ⚠ 早前一轮据 `pg_column_size(jsonb)` 得出「12.29× 溢写」的结论**是量具
--   口径错**：pg_column_size(jsonb) 返回**存储体积**（解压后），而 Sort 搬运
--   的是**元组内联字节**（TOASTed 值只占 18 字节指针）。该结论已作废。
--
-- 【★ 60s 的真正去向：写侧】
--   154 日志实测：05:29:08.326 的 session_bodies_hot 批次移动 511 行，
--   从上一条 session_turn_details_hot 日志（05:28:27.873）算起**耗时 40.45s**
--   ⇒ **≈79ms/行**。而选取侧 30ms/4770行 ≈ **0.006ms/行** ⇒ ⇒
--   ⇒ **99.99% 的时间在「INSERT 进父表 + DELETE 出 hot」**。
--   父表 session_bodies_2026_10 = 5,381 MB / 290,633 行（heap，非 columnar）。
--   ⇒ 本迁移（补 ts 索引）**不能**单独消除 60s；它消除的是
--   **「批次选取随表体量线性劣化」这个放大器** —— 今天的 4770 行只花 30ms，
--   明天 50 万行就是另一回事。索引是**前置条件**，不是 60s 的解。
--
-- 【★ 交付缺口：614 的 ts 索引从未在生产落地，而台账说它已应用】
--   614 @ 6973a9fb8（= 生产 schema_migrations 记账时刻 2026-08-29 16:48:05
--   那一版）的定义是：
--       CREATE INDEX idx_session_bodies_hot_ts
--           ON public.session_bodies_hot(ts)
--           WHERE ts > now() - interval '8 hours';     ← ★ now() 非 IMMUTABLE
--   ⇒ PG 拒绝该谓词（partial index predicate must be immutable）⇒
--   ⇒ **该索引从未建成**；且它**后面**的 idx_session_bodies_hot_partition_date
--   也从未执行（同一文件顺序执行，前面报错就短路）。
--   ⇒ 而它**前面**的 idx_session_bodies_hot_lookup / _request 建成了 ——
--   ⇒ ⇒ 与生产现状**逐条吻合**（生产有 _request、无 ts、无 partition_date；
--   lookup 后来被 888 显式 DROP）。
--   ⇒ 2026-09-03 的 368202126 修掉了 now() 谓词（改成裸 (ts)），但
--   ⇒ **614 的台账已记为 applied ⇒ 修复永远到不了生产**。
--   ⇒ 且 614 **不在本通道 files=() 里** ⇒ 通道也不补 ⇒ **双通道都到不了**。
--   ⇒ ⇒ 这就是「台账说已应用 ≠ 实际对象是那一版」的**带精确失败点**的实例。
--
-- 【★ 另一条独立通道也拿不到它】
--   707b_session_bodies_hot_prelude.sql（**在**本通道 files=() 里）用
--   `CREATE TABLE public.session_bodies_hot (LIKE public.session_bodies
--   INCLUDING ALL)` 建表 ⇒ 索引集**继承自父表** ⇒ 而父表
--   public.session_bodies 上**没有** ts 首列索引（862 只给 session_turns /
--   _request_logs 的父表加了 idx_*_ts）⇒ ⇒ LIKE 也拷不出 ts 索引。
--   ⇒ ⇒ **两条投递路径都到不了这个索引** ⇒ 必须有一条新迁移直接建它。
--
-- 【形态选择】
--   照抄仓内样板 idx_session_turns_hot_ts (ts, id, partition_date)：
--   session_bodies_hot 有 id 列（614 第 8 行），语义同族 ⇒ 建
--   (ts, id, partition_date) 三列形态，与对照组**逐列一致**。
--   ★ 为什么不是单列 (ts)：批次选取走 `ORDER BY ts LIMIT n`（Go ensure 体），
--   单列 ts 只能让 planner 走 Index Scan 后**再回堆**取 id/partition_date；
--   三列形态可让 deleted CTE 的 `i.id = h.id AND i.partition_date =
--   h.partition_date` 判定**在索引里比完**。★ 三列形态还覆盖 869 那种
--   `ORDER BY h.ts, h.id, h.partition_date` 排序（同族样板）。
--
-- 【为什么用 CONCURRENTLY】
--   session_bodies_hot 是**活写表**（bodies_writer 每秒写入，pg_stat_statements
--   实测 72,813 次 INSERT / mean 20.2ms）⇒ 普通 CREATE INDEX 会在整个构建期
--   持 ShareLock **阻断写入** ⇒ 用 CONCURRENTLY（888 第 2 段同款：
--   hot 表不是分区表 ⇒ 支持 CONCURRENTLY）。
--
-- 【幂等】
--   CREATE INDEX CONCURRENTLY IF NOT EXISTS + 残留自愈（888 第 0 段同款：
--   被 applySQL 的 5 分钟 ctx 掐断会留下 indisvalid=false 的半成品索引，
--   对象在、名字在、**但 planner 不用它**，且 IF NOT EXISTS 对这种对象不生效
--   ⇒ 重跑必须先 DROP）。
--
-- 【Post-condition】
--   索引必须在位、indisvalid=true、indisready=true、首列为 ts。
--
-- 【Down】
--   DROP INDEX CONCURRENTLY IF EXISTS public.idx_session_bodies_hot_ts;
--   ⚠ 这是**不可逆的一步回退**：生产上 614 那条从未落地的索引被本迁移补上后，
--   down 会把它删掉 ⇒ 回到「批次选取 Seq Scan + Sort」的形态。
--   （这是如实登记，不是缺陷。）

-- ── 第 0 段：自愈被 applySQL 的 5 分钟 ctx 掐断的残留 ────────────────
DO $pre_898_heal$
DECLARE
  r record;
BEGIN
  FOR r IN
    SELECT ci.relname
    FROM pg_index i
    JOIN pg_class ci ON ci.oid = i.indexrelid
    JOIN pg_class ct ON ct.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = ct.relnamespace
    WHERE n.nspname = 'public'
      AND ct.relname = 'session_bodies_hot'
      AND NOT i.indisvalid
      AND ci.relname = 'idx_session_bodies_hot_ts'
  LOOP
    EXECUTE format('DROP INDEX IF EXISTS public.%I', r.relname);
    RAISE NOTICE '898: dropped leftover invalid index public.%', r.relname;
  END LOOP;
END
$pre_898_heal$;

-- ── 第 1 段：建索引（hot 表非分区 ⇒ 单条 CONCURRENTLY）────────────────
--
-- ★ ★ 等锁保护（★ installer 的 startup_ddl_lock_guard_gate_test.go 的硬要求：
-- ★ ★ >cutoff 的强锁 DDL 迁移必须带 lock_timeout ⇒ ⇒ 否则它可能在等锁期间
-- ★ ★ 堵住全库）。
-- ★ ★ 为什么用【会话级】 SET 而不是 SET LOCAL：★ 本文件带
-- ★ ★ dbinit:no-transaction 标记 ⇒ 走**非事务通道** ⇒ ★ 没有事务块 ⇒
-- ★ ★ SET LOCAL 无意义（★ 877 同款：sql/migrations/startup/877 的第 78-79 行
-- ★ ★ 逐字记着这条理由）。
-- ★ ★ 抢不到锁 ⇒ 整体失败、什么都没发生 ⇒ ★ 稍后重跑即可 ⇒ ★ 不会留下
-- ★ ★ 半成品（★ 第 0 段的残留自愈负责清理被 ctx 掐断的半成品）。
SET lock_timeout = '60s';
SET statement_timeout = '10min';

SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.session_bodies_hot USING btree (ts, id, partition_date)',
  'idx_session_bodies_hot_ts')
\gexec

-- ── 形状校验：索引必须在位、valid、ready、首列 ts ─────────────────────
DO $verify_898$
DECLARE
  v_missing boolean;
  v_invalid  boolean;
  v_notready boolean;
  v_firstcol text;
BEGIN
  SELECT NOT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class ci ON ci.oid = i.indexrelid
    JOIN pg_class ct ON ct.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = ct.relnamespace
    WHERE n.nspname = 'public'
      AND ct.relname = 'session_bodies_hot'
      AND ci.relname = 'idx_session_bodies_hot_ts'
  )
  INTO v_missing;

  SELECT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class ci ON ci.oid = i.indexrelid
    JOIN pg_class ct ON ct.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = ct.relnamespace
    WHERE n.nspname = 'public'
      AND ct.relname = 'session_bodies_hot'
      AND ci.relname = 'idx_session_bodies_hot_ts'
      AND NOT i.indisvalid
  )
  INTO v_invalid;

  SELECT EXISTS (
    SELECT 1 FROM pg_index i
    JOIN pg_class ci ON ci.oid = i.indexrelid
    JOIN pg_class ct ON ct.oid = i.indrelid
    JOIN pg_namespace n ON n.oid = ct.relnamespace
    WHERE n.nspname = 'public'
      AND ct.relname = 'session_bodies_hot'
      AND ci.relname = 'idx_session_bodies_hot_ts'
      AND NOT i.indisready
  )
  INTO v_notready;

  SELECT a.attname INTO v_firstcol
  FROM pg_index i
  JOIN pg_class ci ON ci.oid = i.indexrelid
  JOIN pg_class ct ON ct.oid = i.indrelid
  JOIN pg_namespace n ON n.oid = ct.relnamespace
  JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS u(k, ord) ON u.ord = 1
  JOIN pg_attribute a ON a.attrelid = ct.oid AND a.attnum = u.k
  WHERE n.nspname = 'public'
    AND ct.relname = 'session_bodies_hot'
    AND ci.relname = 'idx_session_bodies_hot_ts';

  IF v_missing THEN
    RAISE EXCEPTION '898 failed: public.idx_session_bodies_hot_ts missing on public.session_bodies_hot';
  END IF;
  IF v_invalid THEN
    RAISE EXCEPTION '898 failed: public.idx_session_bodies_hot_ts exists but indisvalid=false (CONCURRENTLY build did not complete; re-run 898 to rebuild)';
  END IF;
  IF v_notready THEN
    RAISE EXCEPTION '898 failed: public.idx_session_bodies_hot_ts exists but indisready=false (CONCURRENTLY build did not complete; re-run 898 to rebuild)';
  END IF;
  IF v_firstcol IS DISTINCT FROM 'ts' THEN
    RAISE EXCEPTION '898 failed: public.idx_session_bodies_hot_ts leading column is %, expected ts', coalesce(v_firstcol, '<null>');
  END IF;

  RAISE NOTICE '898: public.idx_session_bodies_hot_ts (ts, id, partition_date) is in place and valid on public.session_bodies_hot';
END
$verify_898$;
