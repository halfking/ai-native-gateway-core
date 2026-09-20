-- 728: 252 PG SQL 日志审计复核轮（2026-09-21）慢查询索引修复。
-- 证据来源：pg-252-pg17 生产 stderr 慢日志（log_min_duration_statement=1s，
-- 6.3h 窗口快照）+ pg_stat_statements（stats_reset 2026-09-11 起 10 天累计）
-- + 真库 EXPLAIN A/B，详见 docs/audit/2026-09-21-252-pg-sql-log-recheck.md。
--
-- F | request_logs 分区侧缺 credential+model 表达式索引（热/冷双侧不对齐）：
--   provider-model 抽屉的按凭据+模型轮询查询
--     SELECT ... FROM request_logs_with_current_month
--     WHERE credential_id = $1
--       AND lower(COALESCE(outbound_model, client_model)) = lower($2)
--       AND ts > NOW() - $window ORDER BY ts DESC LIMIT $n
--   的 hot 分支自 idx_request_logs_hot_credential_model_ts
--   （sql/objects，schema 通道管理）建成后走 Index Scan，但父表与全部存量
--   月分区无任何 credential_id 索引——查询窗口一旦越过 hot/promote 边界
--   伸入当月分区，该分支即 Parallel Seq Scan。252 实测（pg_stat_statements，
--   10 天）：184,620 次、均值 168ms、max 13.5s、累计 8.6h＝全库总耗时
--   第一名；慢日志 6.3h 窗口 717 次（sum 1207s）；72h 窗口 EXPLAIN ANALYZE
--   实测 105s（应用侧 30s 角色级 statement_timeout 之前早已被取消）。
--   同一索引亦覆盖按 credential_id + ts 过滤当月分区的候选失败关联查询
--   （慢日志 n=119 均值 1.8s / n=82 均值 2.1s 两族）。
--
-- 实现约束与 727 相同：PG17 不支持在分区父表上 CREATE INDEX CONCURRENTLY
-- （42809），采用标准三段式：
--   1) 对每个"已存在"的分区单独 CONCURRENTLY 建（\gexec 生成，全新库无
--      分区时自然为空操作，不阻塞写入）；
--   2) 父表 ONLY 建分区索引壳（无扫描，仅短暂元数据锁）；
--   3) 逐个 ATTACH 子索引（幂等 DO 守卫）。
-- 未来月分区经 PARTITION OF 自动继承父索引；hot 表索引由 sql/objects
-- 通道负责（本迁移不重复建，IF NOT EXISTS 亦不必）。fresh install 走
-- startup 序列执行本迁移，同样得到父壳 + 后续分区继承。
--
-- 表达式列与热表索引逐字一致（lower(COALESCE(outbound_model, client_model))，
-- 均 immutable，可建索引），planner 才会在视图 UNION 分支上复用。

-- ── 第 1 段：对已存在的 request_logs 分区逐个 CONCURRENTLY 建 ──────────
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I (credential_id, (lower(COALESCE(outbound_model, client_model))), ts DESC)',
  c.relname || '_credential_model_ts', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.request_logs'::regclass
)
ORDER BY c.relname
\gexec

-- ── 第 2 段：父表分区索引壳 ────────────────────────────────────────────
CREATE INDEX IF NOT EXISTS idx_request_logs_credential_model_ts
  ON ONLY public.request_logs (credential_id, (lower(COALESCE(outbound_model, client_model))), ts DESC);

-- ── 第 3 段：ATTACH 已建好的子索引（幂等守卫）──────────────────────────
DO $$
DECLARE
  part text;
BEGIN
  FOR part IN
    SELECT c.relname
    FROM pg_inherits i
    JOIN pg_class c ON c.oid = i.inhrelid
    WHERE i.inhparent = 'public.request_logs'::regclass
  LOOP
    IF to_regclass(format('public.%I', part || '_credential_model_ts')) IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         WHERE ci.inhparent = 'public.idx_request_logs_credential_model_ts'::regclass
           AND ci.inhrelid = to_regclass(format('public.%I', part || '_credential_model_ts'))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_request_logs_credential_model_ts ATTACH PARTITION public.%I',
                     part || '_credential_model_ts');
    END IF;
  END LOOP;
END $$;
