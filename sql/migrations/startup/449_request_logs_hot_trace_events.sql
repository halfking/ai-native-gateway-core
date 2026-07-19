-- Migration: 449_request_logs_hot_trace_events
-- Purpose: request_logs_hot 增加 trace_events JSONB 列,补齐 migration 420 的遗漏
-- Author: ACC Agent
-- Date: 2026-07-20
-- Refs: docs/design/request-trace-system.md
--
-- Background:
--   Migration 420 (2026-07-17) added `trace_events JSONB` to the parent
--   table `request_logs`. However, `request_logs_hot` is a SEPARATE
--   physical table (introduced in migration 341 "hot_table_independence"),
--   NOT a partition of request_logs, so it did not inherit the column.
--
--   Symptom: trace.FlushToPG (internal/trace/trace.go:482) runs
--     UPDATE request_logs_hot SET trace_events = $1::jsonb
--   on every request and has been failing with:
--     SQLSTATE 42703 (undefined_column):
--     column "trace_events" of relation "request_logs_hot" does not exist
--   Observed 3656 times in gateway.log on 2026-07-20 across the 03:30+
--   build_seq 1199 deploy window, plus a long tail of ColumnarScan errors
--   from earlier writes.
--
-- Fix:
--   1. ADD COLUMN IF NOT EXISTS on request_logs_hot (idempotent).
--   2. Mirror the partial index created in migration 420 for the parent.
--
-- Note: request_logs_hot is the hot-tier write target used by the
-- telemetry WAL pipeline. Without trace_events it cannot persist the
-- in-flight trace JSON, so the request-trace dashboard renders as empty
-- for any row whose stage events are still being recorded.

ALTER TABLE request_logs_hot
  ADD COLUMN IF NOT EXISTS trace_events JSONB;

COMMENT ON COLUMN request_logs_hot.trace_events IS
  '2026-07-20: 请求链路追踪事件数组,补齐 migration 420 对 request_logs_hot 的遗漏。'
  '格式与 request_logs.trace_events 一致;写入逻辑见 internal/trace.RedisRecorder.FlushToPG.';

CREATE INDEX IF NOT EXISTS idx_request_logs_hot_has_trace_events
  ON request_logs_hot (request_id)
  WHERE trace_events IS NOT NULL;