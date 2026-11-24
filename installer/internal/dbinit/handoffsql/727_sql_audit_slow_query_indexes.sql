-- 727: 252 PG SQL 日志审计轮（2026-09-20）慢查询索引修复。
-- 证据来源：pg-252-pg17 生产 stderr 日志（ctr.log 快照）+ pg_stat_statements
-- （stats_reset 2026-09-11 起 9 天窗口）+ 真库 EXPLAIN A/B，详见
-- docs/audit/2026-09-20-pg-sql-log-audit/。
--
-- F-A | request_stage_events 保留清理 DELETE 无 created_at 索引：
--   internal/trace/stage_events_retention.go deleteBatch 子查询
--   `WHERE created_at < NOW() - $1 LIMIT $2` 全表扫 1.27GB（表 151k 行但
--   行均 ~8.4KB），pg_stat_statements 实测 9 天 532 批、均值 10.5s、max
--   29.7s 撞 30s 批超时（canceling statement 风暴来源之一）。该文件头部
--   注释（2026-09-05）已预登记"若生产实测扫描成本高，再补 created_at
--   索引迁移，本 worker 的 SQL 无需变化"——本轮即该条件达成。
--
-- F-B | 会话存在性 EXISTS 查询对 session_turns 两侧全表扫：
--   request_logs_with_current_month 的 session_turns 分支（视图链冻结，
--   R37 定案不可改视图）以
--     CASE WHEN session_id LIKE 'sys:%' THEN NULL ELSE session_id END = $gw
--   过滤；该表达式无索引支撑时 session_turns_hot（16k 行 cost 4476 Seq
--   Scan）与当月分区（cost 78304 Seq Scan）全表扫，252 实测 P50 687ms、
--   max 29.8s（statement_timeout 边缘），且该查询在生产日志中被反复取消。
--   建立与视图投影结构一致的函数索引后，planner 可从
--   "CASE 表达式 = 非空常量" 恒等推导出 session_id = 常量（并复用既有
--   (tenant_id, session_id) 前缀索引），真库 A/B：687ms → 1.1ms。
--   注意 session_turns_hot 是独立 hot 表（非 session_turns 的分区，
--   dual-write 架构），父表索引不级联，必须单独建。
--
-- 实现约束：PG17 仍不支持在分区父表上 CREATE INDEX CONCURRENTLY（252
-- 真库实测 42809）。因此采用标准三段式：
--   1) 对每个"已存在"的分区单独 CONCURRENTLY 建（\gexec 生成，全新库无
--      分区时自然为空操作，不阻塞写入）；
--   2) 父表 ONLY 建分区索引壳（无扫描，仅短暂元数据锁）；
--   3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
-- 之后 ensure_sessions_v2_partitions() 以 CREATE TABLE ... PARTITION OF 建
-- 的未来月分区会自动继承父表分区索引，无需模板动作。
--
-- 2026-09-20 已于 252 存量真库实跑验证（IF NOT EXISTS 幂等，与 726 同款
-- "实跑后随二进制 no-op" 交付路径）。

-- ── F-A：stage events 保留清理 ──────────────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_stage_events_created_at
  ON public.request_stage_events (created_at);

-- ── F-B 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ──────
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I (tenant_id, (CASE WHEN session_id LIKE ''sys:%%'' THEN NULL::text ELSE session_id END))',
  c.relname || '_effective_session', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
)
ORDER BY c.relname
\gexec

-- ── F-B 第 2 段：session_turns_hot 独立 hot 表（普通表，CONCURRENTLY）──
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_hot_effective_session
  ON public.session_turns_hot (tenant_id, (CASE WHEN session_id LIKE 'sys:%' THEN NULL::text ELSE session_id END));

-- ── F-B 第 3 段：父表分区索引壳 + ATTACH 已建好的子索引 ────────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_effective_session
  ON ONLY public.session_turns (tenant_id, (CASE WHEN session_id LIKE 'sys:%' THEN NULL::text ELSE session_id END));

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
    IF to_regclass(format('public.%I', part || '_effective_session')) IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         WHERE ci.inhparent = 'public.idx_session_turns_effective_session'::regclass
           AND ci.inhrelid = to_regclass(format('public.%I', part || '_effective_session'))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_effective_session ATTACH PARTITION public.%I',
                     part || '_effective_session');
    END IF;
  END LOOP;
END $$;
