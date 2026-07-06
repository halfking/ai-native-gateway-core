-- 20260706_goal_loop_detection.sql
-- Adds loop-detection & model-switch tracking columns to goal_sessions.
--
-- When the goal feature detects that a session is stuck (either the
-- auto_continue budget is exhausted, or the model keeps emitting the same
-- response), it can rotate to a configured fallback model and give the new
-- model a fresh continue budget. These columns persist that state so it
-- survives across requests and is visible to operators.
--
-- Columns are nullable/COALESCE-defaulted so existing rows and any deployment
-- that hasn't run this migration keep working (the Go store reads them via
-- COALESCE(..., 0) / COALESCE(..., '')).
--
-- Idempotent: ADD COLUMN IF NOT EXISTS.

ALTER TABLE goal_sessions
    ADD COLUMN IF NOT EXISTS model_switch_count INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS repeat_count        INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_response_hash  VARCHAR(64) DEFAULT '',
    ADD COLUMN IF NOT EXISTS current_model       VARCHAR(128) DEFAULT '';

COMMENT ON COLUMN goal_sessions.model_switch_count IS
    'Number of times the goal hook rotated the session to a fallback model after detecting a loop. Bounded by goal.max_model_switch_count.';
COMMENT ON COLUMN goal_sessions.repeat_count IS
    'Consecutive identical assistant replies observed. When it reaches goal.repeat_threshold the session is considered stuck and a model switch may be triggered.';
COMMENT ON COLUMN goal_sessions.last_response_hash IS
    'sha256 (hex) of the last assistant content, used to detect repeated responses.';
COMMENT ON COLUMN goal_sessions.current_model IS
    'The model currently driving this goal session. Changes on each model rotation; empty means the original client model is in use.';
