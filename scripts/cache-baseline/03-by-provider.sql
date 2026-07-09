-- 缓存命中率基线诊断 - 按 Provider 分组
-- 文件: scripts/cache-baseline/03-by-provider.sql
-- 用途: 识别 provider 级缓存支持差异
-- 数据源: request_logs + providers
-- 时间窗: 最近 7 天

SELECT
  p.display_name AS provider,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE NULL
  END AS hit_ratio
FROM request_logs r
JOIN providers p ON p.id = r.provider_id
WHERE r.ts >= NOW() - INTERVAL '7 days'
  AND r.success = true
GROUP BY p.display_name
ORDER BY request_count DESC;