-- 433_system_metrics_local_ingest.sql
-- 2026-07-19: 本地采集系统指标（CPU/内存/磁盘）并保存到 runtime_metrics
--
-- 背景:
--   internal/collector 当前仅向 Authority 上报 (HTTPReporter)。
--   为了诊断负载问题（如 P99 latency 7566ms 是否由系统资源瓶颈导致），
--   需要在本地 PG 保存采集的指标，与 request_logs 时间戳对齐分析。
--
--   runtime_metrics 表已存在（migration 402），本迁移：
--   1. 确保 runtime_metrics 表有完整索引
--   2. 添加 system_metrics_local 视图（最近 24 小时聚合）
--   3. 添加便利查询函数
--
--   实现策略：
--   - collector 通过新增 LocalDBReporter 同时写本地和 Authority
--   - 保留周期：默认 30 天（settings.lifecycle.runtime_metrics_ttl_days）
--   - 清理：partition_manager.cleanupOldRuntimeMetrics 已有逻辑

BEGIN;

-- runtime_metrics 表已存在（402 迁移），补充完整索引
CREATE INDEX IF NOT EXISTS idx_runtime_metrics_timestamp
    ON runtime_metrics (timestamp DESC);

CREATE INDEX IF NOT EXISTS idx_runtime_metrics_cpu_usage
    ON runtime_metrics (timestamp DESC, cpu_usage_pct DESC)
    WHERE cpu_usage_pct > 70.0;

CREATE INDEX IF NOT EXISTS idx_runtime_metrics_mem_usage
    ON runtime_metrics (timestamp DESC, mem_used_mb DESC);

-- 最近 24 小时系统指标聚合视图（用于快速排查）
CREATE OR REPLACE VIEW system_metrics_recent AS
SELECT
    instance_id,
    date_trunc('minute', timestamp) AS time_bucket,
    AVG(cpu_usage_pct) AS avg_cpu_pct,
    MAX(cpu_usage_pct) AS max_cpu_pct,
    AVG(mem_used_mb::float / NULLIF(mem_total_mb, 0) * 100) AS avg_mem_pct,
    MAX(mem_used_mb::float / NULLIF(mem_total_mb, 0) * 100) AS max_mem_pct,
    AVG(disk_used_gb::float / NULLIF(disk_total_gb, 0) * 100) AS avg_disk_pct,
    MAX(current_concurrency) AS max_concurrency,
    AVG(last_5min_tps) AS avg_tps,
    PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY last_5min_p50_ms) AS p50_latency_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY last_5min_p99_ms) AS p99_latency_ms,
    AVG(last_5min_success_pct) AS avg_success_pct,
    COUNT(*) AS sample_count
FROM runtime_metrics
WHERE timestamp >= now() - INTERVAL '24 hours'
GROUP BY instance_id, date_trunc('minute', timestamp)
ORDER BY time_bucket DESC;

COMMENT ON VIEW system_metrics_recent IS 
'最近 24 小时系统指标聚合（按分钟）。用于排查负载与延迟相关性。';

-- 便利函数：获取指定时间窗口的系统快照
CREATE OR REPLACE FUNCTION get_system_snapshot(
    p_instance_id TEXT DEFAULT NULL,
    p_hours_ago INT DEFAULT 1
)
RETURNS TABLE (
    timestamp TIMESTAMPTZ,
    cpu_usage_pct REAL,
    mem_usage_pct REAL,
    disk_usage_pct REAL,
    current_concurrency INT,
    tps REAL,
    p50_ms REAL,
    p99_ms REAL,
    success_pct REAL
) AS $$
BEGIN
    RETURN QUERY
    SELECT
        rm.timestamp,
        rm.cpu_usage_pct,
        (rm.mem_used_mb::float / NULLIF(rm.mem_total_mb, 0) * 100)::REAL AS mem_usage_pct,
        (rm.disk_used_gb::float / NULLIF(rm.disk_total_gb, 0) * 100)::REAL AS disk_usage_pct,
        rm.current_concurrency,
        rm.last_5min_tps,
        rm.last_5min_p50_ms,
        rm.last_5min_p99_ms,
        rm.last_5min_success_pct
    FROM runtime_metrics rm
    WHERE rm.timestamp >= now() - (p_hours_ago || ' hours')::INTERVAL
      AND (p_instance_id IS NULL OR rm.instance_id = p_instance_id)
    ORDER BY rm.timestamp DESC;
END;
$$ LANGUAGE plpgsql STABLE;

COMMENT ON FUNCTION get_system_snapshot IS 
'获取最近 N 小时的系统指标快照。默认 1 小时。用于与 request_logs 延迟数据对齐分析。';

COMMIT;
