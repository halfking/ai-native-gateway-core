-- 缓存命中率基线诊断 - 粘性亲和性 vs 缓存命中率
-- 文件: scripts/cache-baseline/06-affinity-vs-cache.sql
-- 用途: 关联粘性命中与缓存命中（核心证据）
-- 数据源: request_logs
-- 时间窗: 最近 7 天

SELECT
  CASE WHEN affinity_hit THEN 'affinity_hit' ELSE 'affinity_miss' END AS affinity,
  COUNT(*) AS request_count,
  SUM(COALESCE(cache_read_tokens, 0)) AS cache_read,
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
GROUP BY affinity_hit;

-- 解读：
-- 如果 affinity_hit 命中率显著高于 affinity_miss（差 > 10pp）
--   → 凭据稳定性确实影响缓存，需要优先优化粘性路由
-- 如果两者相近
--   → 缓存主要依赖前缀稳定性而非凭据稳定性（审计核心结论成立）