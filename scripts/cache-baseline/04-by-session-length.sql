-- 缓存命中率基线诊断 - 按会话长度分组
-- 文件: scripts/cache-baseline/04-by-session-length.sql
-- 用途: 多轮对话 vs 单轮请求的缓存效果差异
-- 数据源: request_logs
-- 时间窗: 最近 7 天

WITH session_stats AS (
  SELECT
    session_id,
    COUNT(*) AS turn_count,
    SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
    SUM(COALESCE(prompt_tokens, 0)) AS prompt
  FROM request_logs
  WHERE ts >= NOW() - INTERVAL '7 days'
    AND success = true
    AND session_id IS NOT NULL
    AND session_id != ''
  GROUP BY session_id
)
SELECT
  CASE
    WHEN turn_count = 1 THEN '1_turn'
    WHEN turn_count BETWEEN 2 AND 5 THEN '2-5_turns'
    WHEN turn_count BETWEEN 6 AND 10 THEN '6-10_turns'
    WHEN turn_count BETWEEN 11 AND 20 THEN '11-20_turns'
    ELSE '21+_turns'
  END AS session_length_bucket,
  COUNT(*) AS session_count,
  ROUND(AVG(cache_read), 0) AS avg_cache_read,
  CASE
    WHEN SUM(COALESCE(cache_read,0)) + SUM(COALESCE(prompt,0)) > 0
    THEN ROUND(
      SUM(cache_read)::numeric /
      NULLIF(SUM(cache_read) + SUM(prompt), 0), 4
    )
    ELSE 0
  END AS hit_ratio
FROM session_stats
GROUP BY 1
ORDER BY 1;