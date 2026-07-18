-- 434_request_stage_events_table.sql
-- 2026-07-19: 请求阶段事件规范化表（可选，补充 trace_events JSONB）
--
-- 背景:
--   trace_events (JSONB 列) 已存在（migration 420），存储完整的 TraceEvent 数组。
--   本迁移新增规范化表 request_stage_events，用于：
--   1. 高效查询特定阶段的失败（WHERE stage='upstream_request' AND status='failed'）
--   2. 阶段级聚合统计（AVG(duration_ms) GROUP BY stage）
--   3. 与 Redis cache 状态对齐（每阶段是否有缓存命中）
--
--   与 trace_events 的关系：
--   - trace_events = 原始完整记录（JSON），用于详细回溯
--   - request_stage_events = 扁平化索引表，用于快速查询和聚合
--   - 数据来源：RedisRecorder.FlushToPG 时同时写入两者（或通过 trigger）
--
--   实现策略：
--   - 初期：通过 Go 代码在 FlushToPG 时解析 trace_events 并写入
--   - 后期可选：添加 trigger 自动同步（性能优化后）

BEGIN;

-- 请求阶段事件表（扁平化，便于查询和聚合）
CREATE TABLE IF NOT EXISTS request_stage_events (
    id                BIGSERIAL PRIMARY KEY,
    request_id        TEXT NOT NULL,
    tenant_id         TEXT NOT NULL,
    seq               INT NOT NULL,                -- 事件序号（对应 TraceEvent.Seq）
    stage             TEXT NOT NULL,               -- upstream_request, stream_complete, etc.
    stage_name        TEXT,                        -- 中文显示名
    module            TEXT,                        -- middleware, upstream, handler, etc.
    timestamp         TIMESTAMPTZ NOT NULL,
    duration_ms       INT,
    status            TEXT NOT NULL,               -- success, failed, timeout, skipped
    error_message     TEXT,
    
    -- 上游错误详情（对应 5xx 详细诊断需求）
    http_status       INT,
    response_body     TEXT,                        -- 上游响应体（截断 512 字节）
    failure_hint      TEXT,                        -- classifyUpstreamError 结果
    
    -- 扩展字段（通用 details）
    details           JSONB,
    
    -- 快照（失败时）
    snapshot          JSONB,
    
    -- Redis 缓存状态（可选，用于诊断缓存命中/未命中）
    redis_hit         BOOLEAN,
    redis_key         TEXT,
    
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 索引：按 request_id 查询完整链路
CREATE INDEX IF NOT EXISTS idx_stage_events_request_id
    ON request_stage_events (request_id, seq);

-- 索引：按 tenant + 时间查询
CREATE INDEX IF NOT EXISTS idx_stage_events_tenant_ts
    ON request_stage_events (tenant_id, timestamp DESC);

-- 索引：按阶段 + 状态查询（诊断特定阶段失败）
CREATE INDEX IF NOT EXISTS idx_stage_events_stage_status
    ON request_stage_events (stage, status, timestamp DESC)
    WHERE status IN ('failed', 'timeout');

-- 索引：上游失败（5xx 详细诊断）
CREATE INDEX IF NOT EXISTS idx_stage_events_upstream_failure
    ON request_stage_events (stage, http_status, timestamp DESC)
    WHERE stage = 'upstream_request' AND http_status >= 500;

-- 索引：Redis 缓存未命中（诊断缓存效率）
CREATE INDEX IF NOT EXISTS idx_stage_events_redis_miss
    ON request_stage_events (stage, timestamp DESC)
    WHERE redis_hit = FALSE;

COMMENT ON TABLE request_stage_events IS 
'请求阶段事件扁平化表。补充 trace_events JSONB，用于高效查询和聚合。';

COMMENT ON COLUMN request_stage_events.response_body IS 
'上游响应体（失败时），截断 512 字节。完整 body 见 candidate_failure_logs.upstream_response_body。';

COMMENT ON COLUMN request_stage_events.redis_hit IS 
'该阶段是否有 Redis 缓存命中（可选字段，用于诊断缓存效率）。';

-- 聚合视图：各阶段平均耗时和失败率（最近 1 小时）
CREATE OR REPLACE VIEW stage_performance_recent AS
SELECT
    stage,
    COUNT(*) AS total_events,
    AVG(duration_ms) AS avg_duration_ms,
    PERCENTILE_CONT(0.50) WITHIN GROUP (ORDER BY duration_ms) AS p50_duration_ms,
    PERCENTILE_CONT(0.99) WITHIN GROUP (ORDER BY duration_ms) AS p99_duration_ms,
    COUNT(*) FILTER (WHERE status = 'failed') AS failed_count,
    COUNT(*) FILTER (WHERE status = 'timeout') AS timeout_count,
    COUNT(*) FILTER (WHERE status = 'success') AS success_count,
    (COUNT(*) FILTER (WHERE status = 'failed')::float / NULLIF(COUNT(*), 0) * 100) AS failure_rate_pct,
    COUNT(*) FILTER (WHERE redis_hit = TRUE) AS redis_hit_count,
    COUNT(*) FILTER (WHERE redis_hit = FALSE) AS redis_miss_count
FROM request_stage_events
WHERE timestamp >= now() - INTERVAL '1 hour'
GROUP BY stage
ORDER BY avg_duration_ms DESC NULLS LAST;

COMMENT ON VIEW stage_performance_recent IS 
'最近 1 小时各阶段性能统计（平均耗时、P50/P99、失败率、缓存命中率）。';

-- 聚合视图：上游 5xx 错误分布（最近 1 小时）
CREATE OR REPLACE VIEW upstream_5xx_distribution AS
SELECT
    http_status,
    failure_hint,
    COUNT(*) AS error_count,
    COUNT(DISTINCT request_id) AS affected_requests,
    array_agg(DISTINCT details->>'credential_id') FILTER (WHERE details ? 'credential_id') AS affected_credentials,
    array_agg(DISTINCT details->>'raw_model') FILTER (WHERE details ? 'raw_model') AS affected_models,
    MIN(timestamp) AS first_seen,
    MAX(timestamp) AS last_seen,
    array_agg(response_body) FILTER (WHERE response_body IS NOT NULL) AS sample_bodies
FROM request_stage_events
WHERE stage = 'upstream_request'
  AND status = 'failed'
  AND http_status >= 500
  AND timestamp >= now() - INTERVAL '1 hour'
GROUP BY http_status, failure_hint
ORDER BY error_count DESC;

COMMENT ON VIEW upstream_5xx_distribution IS 
'最近 1 小时上游 5xx 错误分布。按状态码和 failure_hint 分组，列出受影响的凭据和模型。';

COMMIT;
