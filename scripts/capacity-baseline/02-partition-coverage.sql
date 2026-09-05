-- 容量基线 2/3：已知分区家族的保留覆盖。
-- 输出：每个分区子表的边界（pg_get_expr）、尺寸、行数估计；
--       据此核对"热窗口边界是否随时间前移"（保留策略是否真实生效）。
-- 家族清单按仓库现状：request_logs / request_logs_hot / request_logs_bodies /
-- request_logs_archive / request_logs_bodies_archive / instance_heartbeats。
-- 用 to_regclass 防护：家族不存在时不报错。
WITH families(family) AS (
  SELECT * FROM (VALUES
    ('request_logs'),
    ('request_logs_hot'),
    ('request_logs_bodies'),
    ('request_logs_archive'),
    ('request_logs_bodies_archive'),
    ('instance_heartbeats')
  ) AS t(name)
),
children AS (
  SELECT
    f.family,
    c.oid::regclass AS partition_name,
    pg_get_expr(c.relpartbound, c.oid) AS partition_bound,
    pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
    pg_total_relation_size(c.oid) AS total_bytes,
    COALESCE(c.reltuples::bigint, 0) AS row_estimate
  FROM families f
  JOIN pg_class parent ON parent.oid = to_regclass(format('public.%I', f.family))
  JOIN pg_inherits i ON i.inhparent = parent.oid
  JOIN pg_class c ON c.oid = i.inhrelid
)
SELECT *
FROM children
ORDER BY family, partition_name;
