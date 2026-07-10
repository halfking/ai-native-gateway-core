-- 缓存命中率基线诊断 - 按模型分组
-- 文件: scripts/cache-baseline/02-by-model.sql
-- 用途: 识别哪些模型缓存命中率最高/最低
-- 数据源: request_logs
-- 时间窗: 最近 7 天

SELECT
  outbound_model AS model,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
  SUM(COALESCE(prompt_tokens, 0)) AS prompt,
  CASE
    WHEN SUM(COALESCE(cache_read_tokens,0)) + SUM(COALESCE(prompt_tokens,0)) > 0
    THEN ROUND(
      SUM(cache_read_tokens)::numeric /
      NULLIF(SUM(cache_read_tokens) + SUM(prompt_tokens), 0), 4
    )
    ELSE 0
  END AS hit_ratio,
  ROUND(SUM(COALESCE(cache_write_tokens, 0))::numeric /
        NULLIF(COUNT(*), 0), 0) AS avg_cache_write_per_req
FROM request_logs
WHERE ts >= NOW() - INTERVAL '7 days'
  AND success = true
  AND prompt_tokens > 100
GROUP BY outbound_model
ORDER BY cache_read DESC
LIMIT 30;