-- 缓存命中率基线诊断 - 按 Prompt 长度分组
-- 文件: scripts/cache-baseline/05-by-prompt-length.sql
-- 用途: 前缀长度对缓存的影响
-- 数据源: request_logs
-- 时间窗: 最近 7 天

SELECT
  CASE
    WHEN prompt_tokens < 500 THEN '0-500'
    WHEN prompt_tokens < 2000 THEN '500-2K'
    WHEN prompt_tokens < 8000 THEN '2K-8K'
    WHEN prompt_tokens < 32000 THEN '8K-32K'
    ELSE '32K+'
  END AS prompt_length_bucket,
  COUNT(*) AS request_count,
  ROUND(AVG(COALESCE(cache_read_tokens, 0))::numeric, 0) AS avg_cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days' AND success = true
GROUP BY 1
ORDER BY 1;