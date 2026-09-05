-- 容量基线 1/3：表与分区尺寸全景。
-- 输出：按总尺寸（堆+索引+TOAST）倒序的前 40 个关系，含行数估计与分区归属。
-- 用途：确定容量基线的"大头"，识别增长异常的关系。
SELECT
  c.oid::regclass                                   AS relation,
  c.relkind                                         AS kind,       -- r=表, p=分区表, m=物化视图
  pg_size_pretty(pg_total_relation_size(c.oid))     AS total_size,
  pg_size_pretty(pg_relation_size(c.oid))           AS heap_size,
  pg_size_pretty(pg_indexes_size(c.oid))            AS index_size,
  COALESCE(c.reltuples::bigint, 0)                  AS row_estimate,
  (SELECT COUNT(*) FROM pg_inherits i WHERE i.inhparent = c.oid) AS child_partitions
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p', 'm')
  AND pg_total_relation_size(c.oid) > 0
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 40;
