-- Migration: 420_request_logs_trace_events
-- Purpose: request_logs 增加 trace_events JSONB 列,用于存储请求链路追踪事件
-- Author: ACC Agent
-- Date: 2026-07-17
-- Refs: docs/design/request-trace-system.md

-- 主表 + 所有分区 + hot 表都会因 ADD COLUMN IF NOT EXISTS 自动获得该列
-- (PostgreSQL 11+ 在分区父表上 ALTER TABLE 会传播到子表)
ALTER TABLE request_logs
  ADD COLUMN IF NOT EXISTS trace_events JSONB;

-- 注释说明该列用途,以及与内部 trace 包的关系
COMMENT ON COLUMN request_logs.trace_events IS
  '2026-07-17: 请求链路追踪事件数组,格式见 internal/trace.RequestTrace。'
  '请求进行中由 Redis 暂存(request:trace:{request_id}, TTL 600s),'
  '请求结束时由 trace.Recorder.FlushToPG 一次性写入此列。';

-- 配套索引: trace_events 用 ? 操作符查询时 GIN 索引更优。
-- 本次迁移仅建 btree 索引以便按 trace_events 是否非空快速过滤(用于后续 dashboard);
-- GIN 索引若需要可单独评估(避免本迁移DDL时间过长影响生产)。
CREATE INDEX IF NOT EXISTS idx_request_logs_has_trace_events
  ON request_logs (request_id)
  WHERE trace_events IS NOT NULL;