-- 缓存命中率基线诊断 - 凭据轮换对缓存的影响
-- 文件: scripts/cache-baseline/07-rotation-vs-cache.sql
-- 用途: 评估凭据轮换对缓存的影响
-- 数据源: request_logs + session_credential_rotations
-- 时间窗: 最近 7 天

WITH rotation_counts AS (
  SELECT
    session_id,
    COUNT(*) AS rotation_count
  FROM session_credential_rotations
  WHERE started_at >= NOW() - INTERVAL '7 days'
  GROUP BY session_id
)
SELECT
  CASE
    WHEN COALESCE(rotation_count, 0) = 0 THEN '0_rotations'
    WHEN rotation_count = 1 THEN '1_rotation'
    WHEN rotation_count BETWEEN 2 AND 5 THEN '2-5_rotations'
    ELSE '6+_rotations'
  END AS rotation_bucket,
  COUNT(DISTINCT r.session_id) AS session_count,
  ROUND(AVG(r.cache_read_tokens), 0) AS avg_cache_read,
  ROUND(AVG(r.prompt_tokens), 0) AS avg_prompt
FROM request_logs r
LEFT JOIN rotation_counts rc ON rc.session_id = r.session_id
WHERE r.ts >= NOW() - INTERVAL '7 days'
  AND r.success = true
  AND r.session_id IS NOT NULL
GROUP BY 1
ORDER BY 1;