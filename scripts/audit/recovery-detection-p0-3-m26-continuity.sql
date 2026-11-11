-- ★★ 关窗前例行核查（★ 2026-11-11 01:52 +08，★ 211 真库）：★ 探针连续性（每分钟 node_probe_runs 计数）
-- ★★ 用途：★ 确认观测窗口内探针未中断；★ 突变分钟（★ 如 01:28–01:33）交给 burst-attrib 归因
-- ★ 关窗前例行核查：探针连续性（每分钟 node_probe_runs 计数）
-- ★ 只读；SET TIME ZONE 保证 +08 口径
SET TIME ZONE 'Asia/Shanghai';

SELECT to_char(date_trunc('minute', created_at), 'MM-DD HH24:MI') AS m,
       count(*) AS n,
       count(*) FILTER (WHERE success) AS ok,
       count(*) FILTER (WHERE NOT success) AS bad
FROM node_probe_runs
WHERE created_at >= now() - interval '150 minutes'
GROUP BY 1
ORDER BY 1;
