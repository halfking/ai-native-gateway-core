-- 491_work_type_default_routes.sql
-- Ensure runtime route columns exist, then seed default task-to-model mappings.
-- Existing administrator-managed route sets remain untouched.
--
-- Runtime joins work_type_model_route to work_type_config and applies the
-- matching l1_task_type preferences in autoroute.WorkTypeRouteStore.

BEGIN;

ALTER TABLE work_type_model_route
    ADD COLUMN IF NOT EXISTS tier TEXT NOT NULL DEFAULT 'secondary';
ALTER TABLE work_type_model_route
    ADD COLUMN IF NOT EXISTS task_quality_score NUMERIC(5,2) NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_wtmr_tier
    ON work_type_model_route (work_type_key, tier, weight DESC);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'work_type_model_route'::regclass
          AND conname = 'work_type_model_route_work_type_key_fkey'
    ) THEN
        ALTER TABLE work_type_model_route
            ADD CONSTRAINT work_type_model_route_work_type_key_fkey
            FOREIGN KEY (work_type_key) REFERENCES work_type_config(key)
            ON DELETE CASCADE NOT VALID;
    END IF;
END $$;

WITH defaults (work_type_key, canonical_name, weight, min_score, enabled, tier) AS (
    VALUES
      ('general_chat',    'glm-5.2',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('general_chat',    'deepseek-chat', 0.90::numeric, 0::numeric, TRUE, 'primary'),
      ('general_chat',    'minimax-m3',    0.80::numeric, 0::numeric, TRUE, 'secondary'),

      ('reasoning',       'deepseek-r1',   1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('reasoning',       'glm-5.2',       0.90::numeric, 0::numeric, TRUE, 'primary'),
      ('reasoning',       'gpt-5.1',       0.80::numeric, 0::numeric, TRUE, 'secondary'),

      ('code_gen',        'deepseek-v3',   1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('code_gen',        'glm-5.2',       0.90::numeric, 0::numeric, TRUE, 'primary'),
      ('code_review',     'deepseek-v3',   1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('code_review',     'glm-5.2',       0.90::numeric, 0::numeric, TRUE, 'primary'),

      ('copywriting',     'glm-5.2',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('copywriting',     'minimax-m3',    0.85::numeric, 0::numeric, TRUE, 'secondary'),
      ('social_post',     'glm-5.2',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('social_post',     'minimax-m3',    0.85::numeric, 0::numeric, TRUE, 'secondary'),

      ('fn_call',         'glm-5.2',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('fn_call',         'gpt-4o',        0.85::numeric, 0::numeric, TRUE, 'secondary'),

      ('long_doc',        'glm-5.2',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('long_doc',        'deepseek-chat', 0.85::numeric, 0::numeric, TRUE, 'secondary'),

      ('agent_workflow',  'gpt-5.1',       1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('agent_workflow',  'glm-5.2',       0.90::numeric, 0::numeric, TRUE, 'primary'),

      ('image_understand','gpt-4o',        1.00::numeric, 0::numeric, TRUE, 'primary'),
      ('image_understand','glm-5.2',       0.85::numeric, 0::numeric, TRUE, 'secondary')
)
INSERT INTO work_type_model_route (work_type_key, canonical_name, weight, min_score, enabled, tier)
SELECT d.work_type_key, d.canonical_name, d.weight, d.min_score, d.enabled, d.tier
FROM defaults d
WHERE EXISTS (
    SELECT 1 FROM work_type_config c
    WHERE c.key = d.work_type_key AND c.enabled = TRUE
)
AND NOT EXISTS (
    SELECT 1 FROM work_type_model_route existing
    WHERE existing.work_type_key = d.work_type_key
      AND existing.canonical_name = d.canonical_name
);

COMMIT;
