-- 480_model_iq_cost_calibration.down.sql
-- Rollback: reset cost_tier to unknown, clear complexity fields,
-- remove v6.1 default routing rows.

\set ON_ERROR_STOP on
BEGIN;

-- Reset models_canonical calibration
UPDATE models_canonical
SET cost_tier = 'unknown',
    complexity_ceiling = NULL,
    min_complexity = NULL,
    updated_at = now()
WHERE modality = 'text'
  AND cost_tier IN ('free', 'low', 'medium', 'high', 'premium');

-- Remove v6.1 default routing
DELETE FROM task_default_routing WHERE reason LIKE 'v6.1 default%';

-- Re-insert v6 default rows (from 477) — best-effort, may reference ghost models
-- but ON CONFLICT DO NOTHING prevents duplicates.
INSERT INTO task_default_routing (task_type, profile, tier, canonical_model, priority, reason)
VALUES
    ('chat', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('reasoning', 'smart', 'primary', 'claude-fable-5', 100, 'v6 default'),
    ('code', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('agent', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('creative', 'smart', 'primary', 'claude-opus-4-8', 100, 'v6 default'),
    ('long_context', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('vision', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default'),
    ('function_call', 'smart', 'primary', 'claude-sonnet-5', 100, 'v6 default')
ON CONFLICT DO NOTHING;

COMMIT;
