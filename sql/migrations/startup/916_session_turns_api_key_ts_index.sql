-- dbinit:no-transaction —— 本文件含 CREATE INDEX CONCURRENTLY（PG 不允许在事务块内执行），installer 的 applySQL 据此走非事务通道（见 installer/internal/dbinit/runner.go noTransactionMarker，887/888/729 同款）。无该标记时全新安装会失败于 "CREATE INDEX CONCURRENTLY cannot run inside a transaction block"。
-- ===========================================================================
-- File:          sql/migrations/startup/916_session_turns_api_key_ts_index.sql
-- Migration:     916
-- Database:      llm_gateway
-- Purpose:       给会话族的 api_key_id **归一表达式**建索引 —— 让「按 api_key 过滤会话」
--                 能用上【索引 seek】而不是【逐行过滤】。
--
-- ── 缺口本体（2026-10-28 R177 轮，252 生产只读实测，非推断）────────────────
-- 读点 admin/usage.go:841-859（密钥用量详情，7 天窗，`api_key_id = 113`）：
--
--   ->  Index Scan using session_turns_2026_10_ts_idx on session_turns_2026_10
--         Index Cond: (ts >= '2026-10-21 …' AND ts < '2026-10-28 …')
--         Filter: (CASE WHEN (api_key_id ~ '^[0-9]+$'::text) THEN (api_key_id)::bigint END = 113)
--         Rows Removed by Filter: 37825
--
-- ⇒ **窗口内 75,449 行被全部读出来，其中只有 37,624 行匹配** ⇒ ⇒
--   ≈ **一半的行是白读的** ⇒ ⇒ 根节点 buffers 实测 **220,401 块**（一次读点）。
--
-- ── 为什么【现有的索引】全都承不住它 ─────────────────────────────────────
-- 252 上名字带 api_key 的索引**只有 4 个**，而它们的**键全是 `(session_id, turn_no DESC)`**，
-- `api_key_id` 只出现在**谓词**里：
--
--   idx_session_turns_api_key_numeric        ON ONLY session_turns (session_id, turn_no DESC) WHERE api_key_id ~ '^[0-9]+$'
--   idx_session_turns_hot_api_key_numeric     ON session_turns_hot (session_id, turn_no DESC) WHERE api_key_id ~ '^[0-9]+$'
--   session_turns_2026_09_session_id_turn_no_idx / session_turns_2026_10_session_id_turn_no_idx1
--
-- ⇒ 它们承的是「**按 session_id 找该会话的轮次**」，不是「按 api_key 找行」。
-- ⇒ ⇒ ★★ **「按 api_key 过滤会话」这条路从来没有被索引承重过。★★**
--
-- ── 为什么必须是【表达式索引】，而不是普通 btree ───────────────────────────
-- canonical 视图 public.request_logs_with_current_month（118 列）把 api_key_id 投影成
--
--   CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END
--
-- （仓内唯一真值源：db/request_logs_view_schema.go 的 SessionFamilyAPIKeyIDExpr）
-- ⇒ ⇒ ① **普通的 btree(api_key_id) 用不上**：谓词是表达式，不是裸列；
-- ⇒ ⇒ ② **部分索引也用不上**：PG 的「谓词蕴含」判定是**语法的** ⇒ 它无法证明
--      「CASE 形式蕴含 `api_key_id ~ '^[0-9]+$'`」⇒ ★ 这正是仓内
--      db/request_logs_view_schema.go 的 SessionFamilyAPIKeyIDNumericPredExpr 注释
--      里已经写死的那条结论（同族先例）。
-- ⇒ ⇒ ⇒ **唯一形态：索引键 = 与视图投影逐字一致的 CASE 表达式。**
--
-- ── 形态选择（与 729 同款）───────────────────────────────────────────────
--   btree ((CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC)
--
--   · 等值前缀 = 归一后的 api_key（与视图投影逐字一致 ⇒ planner 可复用）
--   · ts DESC = 时间窗边界（读点的第二谓词）+ 保序
--   · ★ **不用部分索引**：见上（谓词蕴含是语法的）⇒ 代价是探针流量（api_key_id
--     全 NULL 的行）也会在索引里留下 NULL 条目 ⇒ 这是本形态的**已知代价**，
--     记在这里以便将来复核（替代形态 = 取消视图侧的 CASE 归一，但那是**契约变更**，
--     不在本迁移范围内）。
--
-- ── 实现约束（与 729/887/888 同款）───────────────────────────────────────
--   PG17 不支持在分区父表上 CREATE INDEX CONCURRENTLY（42809），采用标准三段式：
--     1) 对每个**已存在**的分区单独 CONCURRENTLY 建（\gexec 生成，全新库无分区时
--        自然为空操作，不阻塞写入）；
--     2) 父表 ONLY 建分区索引壳（无扫描，仅短暂元数据锁）；
--     3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
--   未来月分区经 PARTITION OF 自动继承父索引。fresh install 走 startup 序列执行本
--   迁移，同样得到父壳 + 后续分区继承。
--
--   ★ session_turns_hot 是独立 hot 表（非分区，dual-write 架构）⇒ 父表索引不级联，
--     必须单独建（729 同款处置）。
--
-- ── 锁守卫（★ 会话级 SET，不是 SET LOCAL）────────────────────────────────
-- ★ 实测（914 文件头，PG 17.6）：`SET LOCAL` 只在事务块内有效；本通道以
--   `psql -f` 逐文件执行（**不在事务块内**）⇒ `SET LOCAL lock_timeout` 只打一条
--   WARNING 后**完全失效** ⇒ ⇒ 这里用**会话级** `SET` ⇒ 两条通道都真的生效。
--   ★ 与 887/888/729 的做法一致（那几个文件含 CONCURRENTLY，本来就不能包事务）。
-- ===========================================================================

-- ── 锁守卫（抢不到锁要快速失败，而不是排在写流量后面）────────────────────
SET lock_timeout = '2s';
SET statement_timeout = '30min';

-- ── 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ──────────
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I ((CASE WHEN api_key_id ~ ''^[0-9]+$'' THEN api_key_id::bigint END), ts DESC)',
  c.relname || '_api_key_ts', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
)
ORDER BY c.relname
\gexec

-- ── 第 2 段：父表分区索引壳 ────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_api_key_ts
  ON ONLY public.session_turns ((CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC);

-- ── 第 3 段：ATTACH 已建好的子索引（幂等守卫）──────────────────────────
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.session_turns'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_api_key_ts')) IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         WHERE ci.inhparent = 'public.idx_session_turns_api_key_ts'::regclass
           AND ci.inhrelid = to_regclass(format('public.%I', part || '_api_key_ts'))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_api_key_ts ATTACH PARTITION public.%I',
                     part || '_api_key_ts');
    END IF;
  END LOOP;
END $$;

-- ── 热侧：session_turns_hot 独立表（729 同款单独建）─────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_hot_api_key_ts
  ON public.session_turns_hot ((CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END), ts DESC);
