-- 缓存命中率基线诊断 - 全局 7 天
-- 文件: scripts/cache-baseline/01-global-7d.sql
-- 用途: 全局缓存命中率基线（C1）
-- 数据源: request_logs
-- 时间窗: 最近 7 天

SELECT
  'request_count' AS metric,
  COUNT(*)::text AS value
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_read_tokens',
  COALESCE(SUM(cache_read_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'prompt_tokens',
  COALESCE(SUM(prompt_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_write_tokens',
  COALESCE(SUM(cache_write_tokens), 0)::text
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
UNION ALL
SELECT 'cache_hit_ratio_7d',
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      (SUM(cache_read_tokens) + SUM(prompt_tokens)), 4
    )::text
    ELSE '0'
  END
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND (cache_read_tokens IS NOT NULL OR prompt_tokens IS NOT NULL);