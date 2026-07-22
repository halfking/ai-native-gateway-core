-- 341b: hotfix for production DBs where migration 341 was only partially applied.
--
-- Symptom (observed on llm.kxpms.cn / DB 172.16.2.210 on 2026-07-22):
--   The "系统监测" dashboard shows 总运行数 = 0 even after running for days,
--   and self_check_runs has had no new rows since 2026-07-18.
--   gateway.log shows, every 5 min:
--     credential_selfcheck_worker: runOne failed credential_id=2
--       pick models: ERROR: function credential_most_used_model(unknown,
--                     integer) does not exist (SQLSTATE 42883)
--
-- Root cause:
--   bg/credential_selfcheck.go pickModels() calls credential_most_used_model()
--   unconditionally (Tier 2 fallback). The function and the
--   self_check_runs.selection_strategy / attempted_models columns are created by
--   migration 341, but on this DB those statements never ran, so every runOne()
--   aborts BEFORE insertRun() → no rows are ever written → 总运行数 stays 0.
--   finalizeRun() would also fail (it UPDATEs those two columns).
--
-- This script is fully idempotent (CREATE OR REPLACE FUNCTION, ADD COLUMN IF
-- NOT EXISTS, DROP CONSTRAINT IF EXISTS) and safe to re-run on any DB. It only
-- restores the subset of 341 that is missing; it does NOT touch tables/columns
-- that already exist. Verified against 341_probe_origin_and_node_probe.sql.

\set ON_ERROR_STOP on
BEGIN;

-- 1. selection_strategy + attempted_models columns on self_check_runs
ALTER TABLE self_check_runs
    ADD COLUMN IF NOT EXISTS selection_strategy text DEFAULT 'most_used',
    ADD COLUMN IF NOT EXISTS attempted_models   jsonb DEFAULT '[]'::jsonb;

COMMENT ON COLUMN self_check_runs.selection_strategy IS
'341: most_used | fallback_<n> | random — which model the credential_selfcheck worker tested';
COMMENT ON COLUMN self_check_runs.attempted_models IS
'341: ordered list of models tried during this run, e.g. ["gpt-4o","gpt-4o-mini","claude-haiku-4-5"]';

ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_selection_strategy_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_selection_strategy_check CHECK (
        selection_strategy IS NULL OR selection_strategy LIKE 'most_used'
                                                       OR selection_strategy LIKE 'fallback_%'
                                                       OR selection_strategy = 'random'
    );

-- Also make the status CHECK accept 'retrying' (credential_selfcheck uses it
-- when 3 fallback models all fail). Idempotent.
ALTER TABLE self_check_runs
    DROP CONSTRAINT IF EXISTS self_check_runs_status_check;
ALTER TABLE self_check_runs
    ADD CONSTRAINT self_check_runs_status_check CHECK (
        status IN ('running','success','partial','failed','retrying')
    );

-- 2. credential_most_used_model(int, int) — the function that was crashing pickModels.
CREATE OR REPLACE FUNCTION credential_most_used_model(
    p_credential_id bigint,
    p_window_hours  integer DEFAULT 24
) RETURNS TABLE (
    raw_model_name text,
    call_count     bigint
)
LANGUAGE SQL
STABLE
AS $$
    SELECT pm.raw_model_name, COUNT(*) AS call_count
    FROM request_logs_hot rl
    JOIN provider_models pm ON pm.id = rl.canonical_id
    WHERE rl.credential_id = p_credential_id
      AND rl.ts >= now() - make_interval(hours => p_window_hours)
      AND rl.success = TRUE
    GROUP BY pm.raw_model_name
    ORDER BY call_count DESC, pm.raw_model_name ASC
    LIMIT 1;
$$;

COMMENT ON FUNCTION credential_most_used_model(bigint, integer) IS
'341: top-1 model by 24h successful traffic for a credential. Used by credential_selfcheck to pick the daily probe model.';

COMMIT;

-- Verify (informational, does not fail the script).
SELECT 'credential_most_used_model' AS object,
       EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
              WHERE proname='credential_most_used_model') AS present
UNION ALL SELECT 'selection_strategy',
       EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='self_check_runs' AND column_name='selection_strategy')
UNION ALL SELECT 'attempted_models',
       EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='self_check_runs' AND column_name='attempted_models');
