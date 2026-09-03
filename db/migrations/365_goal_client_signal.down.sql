BEGIN;

DROP INDEX IF EXISTS public.idx_session_summaries_parent;

ALTER TABLE public.session_summaries
    DROP COLUMN IF EXISTS handoff_reason,
    DROP COLUMN IF EXISTS parent_session_key;

ALTER TABLE public.goal_sessions
    DROP COLUMN IF EXISTS last_sub_agents_report_at,
    DROP COLUMN IF EXISTS sub_agents_pending,
    DROP COLUMN IF EXISTS sub_agents_completed,
    DROP COLUMN IF EXISTS sub_agents_total,
    DROP COLUMN IF EXISTS last_completion_judgement,
    DROP COLUMN IF EXISTS continue_attempt;

COMMIT;
