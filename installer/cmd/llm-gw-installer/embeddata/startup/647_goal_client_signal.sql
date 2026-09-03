-- Migration 647: goal client-signal state and session handoff lineage.
-- Mirrors db/migrations/365_goal_client_signal.sql for startup migration delivery.
BEGIN;

ALTER TABLE public.goal_sessions
    ADD COLUMN IF NOT EXISTS continue_attempt          INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_completion_judgement VARCHAR(32) DEFAULT '',
    ADD COLUMN IF NOT EXISTS sub_agents_total          INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sub_agents_completed      INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS sub_agents_pending        INT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_sub_agents_report_at TIMESTAMPTZ;

COMMENT ON COLUMN public.goal_sessions.continue_attempt IS
    'Number of gw-continue SSE frames already emitted for this session. Bounded by goal.max_auto_continue_count.';
COMMENT ON COLUMN public.goal_sessions.last_completion_judgement IS
    'Last verdict from the completion detector (e.g. structured:status, keyword:done, llm:high, subagent:pending, none).';
COMMENT ON COLUMN public.goal_sessions.sub_agents_total IS
    'Total sub-agents reported by the client via X-Gw-Sub-Agents (latest snapshot).';
COMMENT ON COLUMN public.goal_sessions.sub_agents_completed IS
    'Sub-agents in terminal state per the latest client report.';
COMMENT ON COLUMN public.goal_sessions.sub_agents_pending IS
    'Sub-agents still in non-terminal state per the latest client report. Completion detector treats the task as NOT complete while > 0.';
COMMENT ON COLUMN public.goal_sessions.last_sub_agents_report_at IS
    'Timestamp of the most recent X-Gw-Sub-Agents client report (NULL = never reported).';

ALTER TABLE public.session_summaries
    ADD COLUMN IF NOT EXISTS parent_session_key VARCHAR(255) DEFAULT '',
    ADD COLUMN IF NOT EXISTS handoff_reason VARCHAR(64) DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_session_summaries_parent
    ON public.session_summaries(tenant_id, parent_session_key)
    WHERE parent_session_key <> '';

COMMENT ON COLUMN public.session_summaries.parent_session_key IS
    'Previous session_key when this session was started in response to a gw-handoff frame. Empty for original sessions.';
COMMENT ON COLUMN public.session_summaries.handoff_reason IS
    'Reason recorded at gw-handoff time (context_near_limit, manual_skill, etc.). Mirrors handoff_logs.trigger_reason for the chain entry.';

COMMIT;
