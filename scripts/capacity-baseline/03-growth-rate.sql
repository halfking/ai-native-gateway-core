-- 容量基线 3/3：增长速率估计。
-- 两个视角：
--   A. pg_stat_user_tables 的 n_tup_ins / last_autovacuum —— 自统计重置以来的累计写入与活跃度；
--   B. 时间分区家族的"每月分区尺寸"序列 —— 把分区边界当时间轴，直接读出历史增长斜率。
-- B 是主要依据（无需长期采样），A 用于补充非分区表。
-- A: 非分区大表写入统计（排除分区子表）
SELECT
  c.oid::regclass                                   AS relation,
  COALESCE(s.n_tup_ins, 0)                          AS inserts_since_reset,
  COALESCE(s.n_live_tup, 0)                         AS live_rows,
  s.last_autovacuum,
  pg_size_pretty(pg_total_relation_size(c.oid))     AS total_size
FROM pg_stat_user_tables s
JOIN pg_class c ON c.oid = s.relid
WHERE s.relid IN (
    SELECT c2.oid FROM pg_class c2
    WHERE c2.relkind = 'r'
      AND pg_total_relation_size(c2.oid) > 256 * 1024 * 1024
  )
  AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 20;

-- B: request_logs_archive 月度分区尺寸序列（历史增长斜率）
SELECT
  c.oid::regclass                                   AS partition_name,
  pg_get_expr(c.relpartbound, c.oid)                AS partition_bound,
  pg_size_pretty(pg_total_relation_size(c.oid))     AS total_size,
  COALESCE(c.reltuples::bigint, 0)                  AS row_estimate
FROM pg_inherits i
JOIN pg_class parent ON parent.oid = to_regclass('public.request_logs_archive')
JOIN pg_class c ON c.oid = i.inhrelid
WHERE i.inhparent = parent.oid
ORDER BY partition_name;
