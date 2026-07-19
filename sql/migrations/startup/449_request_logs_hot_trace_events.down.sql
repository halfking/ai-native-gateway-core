-- Migration: 449 down - revert request_logs_hot.trace_events

DROP INDEX IF EXISTS idx_request_logs_hot_has_trace_events;

ALTER TABLE request_logs_hot DROP COLUMN IF EXISTS trace_events;