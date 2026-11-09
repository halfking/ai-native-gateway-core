-- 954: Preserve request traces on the physical probe lane.
-- Probe requests have no business session_turns parent. A nullable column
-- without a default adds metadata only, without scanning or rewriting rows.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

ALTER TABLE public.session_turns_probe_hot
    ADD COLUMN IF NOT EXISTS trace_events jsonb;

COMMENT ON COLUMN public.session_turns_probe_hot.trace_events IS
    'Request trace envelope persisted after the probe parent and normalized stage events are durable; independent of the current probe-lane switch.';

INSERT INTO public.schema_migrations (version, description)
VALUES ('954', 'probe lane trace_events: persist Redis traces on their actual probe parent')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;
COMMIT;
