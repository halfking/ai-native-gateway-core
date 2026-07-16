-- Migration: 420 down - remove trace_events column
ALTER TABLE request_logs DROP COLUMN IF EXISTS trace_events;
DROP INDEX IF EXISTS idx_request_logs_has_trace_events;