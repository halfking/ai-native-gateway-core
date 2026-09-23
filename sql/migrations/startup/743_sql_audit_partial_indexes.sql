-- 743: 252 SQL 日志审计第六轮（2026-09-24）部分索引补课。
-- 证据来源：pg-252-pg17 容器日志 45min 快照（05:17-06:02 CST）+
-- 252 真库 EXPLAIN ANALYZE，详见 docs/audit/2026-09-24-252-sql-log-audit.md。
--
-- A | session_aggregate_outbox done 行 TTL 清理全表扫：
--   domains/session/v2/session_aggregate_outbox_reaper.go trimDoneRows
--     DELETE ... WHERE id IN (SELECT id ... WHERE status='done'
--                             AND completed_at < NOW()-'7 days'
--                             ORDER BY completed_at LIMIT 5000)
--   现有 6 个索引没有 (status='done', completed_at) 组合（pending/claimable/
--   dead 各自的 partial 索引都不覆盖 done），子查询对 645MB 真库全表扫：
--   45min 窗口 ×255 次（3 副本 × 30s tick）med 4.2s max 25s、3 次被 30s
--   rolconfig 击杀——每 10s 烧 ~64MB/s 读 I/O，是整库负载的主要背景噪音。
--   done 行 505k 全在 7 天保留窗内（清理在维持边界，非积压），缺的只是索引。
--
-- B | session_turns digest 回填空扫：
--   domains/session/v2/session_digest_backfill.go
--     SELECT ... FROM session_turns_with_current_month t
--     LEFT JOIN session_bodies_unified b ...
--     WHERE t.digest IS NULL ORDER BY t.ts, t.id LIMIT $1
--   视图 = hot 反连接臂 + session_turns 全分区父表（名字里的
--   current_month 是误导，实际扫全部历史分区）；digest IS NULL 无任何索引，
--   backlog=0 时仍要扫完 68 万行（2026_09）证明空——3 副本 × 30min 空闲
--   backoff ≈ 每 10min 一次重型空扫，45min 窗口 ×6 被 30s rolconfig 击杀，
--   每次击杀还连带孵化 2 个并行 worker（DSM 压力）。
--   部分索引 (ts, id) WHERE digest IS NULL 让空证明变成索引起点即返回，
--   且与查询的 ORDER BY t.ts, t.id 同序，非空时 top-N 也走索引。
--
-- 实现约束与 727/728/729 相同：PG17 不支持在分区父表上
-- CREATE INDEX CONCURRENTLY（42809），session_turns 采用标准三段式；
-- session_aggregate_outbox / session_turns_hot 是普通表，直接 CONCURRENTLY。
-- 未来月分区经 PARTITION OF 自动继承父索引壳的子索引。

-- ── A：outbox done 清理部分索引（普通表）────────────────────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_aggregate_outbox_done_completed_at
  ON public.session_aggregate_outbox (completed_at)
  WHERE status = 'done';

-- ── B 第 1 段：对已存在的 session_turns 分区逐个 CONCURRENTLY 建 ─────────
SELECT format(
  'CREATE INDEX CONCURRENTLY IF NOT EXISTS %I ON public.%I (ts, id) WHERE digest IS NULL',
  c.relname || '_digest_null_idx', c.relname)
FROM pg_class c
WHERE c.oid IN (
  SELECT inhrelid FROM pg_inherits WHERE inhparent = 'public.session_turns'::regclass
)
ORDER BY c.relname
\gexec

-- ── B 第 2 段：父表 ONLY 分区索引壳（无扫描，仅短暂元数据锁）────────────
CREATE INDEX IF NOT EXISTS idx_session_turns_digest_null
  ON ONLY public.session_turns (ts, id) WHERE digest IS NULL;

-- ── B 第 3 段：ATTACH 已建好的子索引（幂等守卫）─────────────────────────
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
    IF to_regclass(format('public.%I', part || '_digest_null_idx')) IS NOT NULL
       AND NOT EXISTS (
         SELECT 1 FROM pg_inherits ci
         WHERE ci.inhparent = 'public.idx_session_turns_digest_null'::regclass
           AND ci.inhrelid = to_regclass(format('public.%I', part || '_digest_null_idx'))
       ) THEN
      EXECUTE format('ALTER INDEX public.idx_session_turns_digest_null ATTACH PARTITION public.%I',
                     part || '_digest_null_idx');
    END IF;
  END LOOP;
END $$;

-- ── B 热侧：session_turns_hot 独立表（727/729 同款单独建）────────────────
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_session_turns_hot_digest_null
  ON public.session_turns_hot (ts, id) WHERE digest IS NULL;

-- 验证：三个索引均已就位（CONCURRENTLY 失败会留 INVALID，由 db.go 的
-- ensureSqlAuditPartialIndexes 在下次 boot 清理重建）。
DO $$
DECLARE
  missing text;
BEGIN
  SELECT string_agg(name, ', ') INTO missing
  FROM (VALUES
    ('idx_session_aggregate_outbox_done_completed_at'),
    ('idx_session_turns_digest_null'),
    ('idx_session_turns_hot_digest_null')
  ) AS v(name)
  WHERE to_regclass(format('public.%I', name)) IS NULL;
  IF missing IS NOT NULL THEN
    RAISE EXCEPTION '743 up: indexes missing after build: %', missing;
  END IF;
END $$;
