-- Migration 549 (down): durable GoalRun coordination ledger rollback draft.
-- ============================================================================
-- ⚠️ CONTRACT DRAFT — DO NOT EXECUTE ⚠️
-- This file mirrors 549_goal_run_ledger.sql as a rollback reference for the
-- Wave 2 GoalRun contract freeze. It is committed solely so the up.sql schema
-- design is self-contained in the contract branch.
--
-- Before implementation:
--   1. Re-validate the migration number against the team's latest numbering
--      (see sql/migrations/startup/ for the next free slot).
--   2. Re-review every policy / index / check constraint — none of this has
--      been applied to any environment.
--   3. Replace this header with the team's standard rollback narrative.
-- ============================================================================

BEGIN;

DROP POLICY IF EXISTS goal_run_action_outbox_super_admin_bypass ON goal_run_action_outbox;
DROP POLICY IF EXISTS goal_run_action_outbox_tenant_isolation ON goal_run_action_outbox;
ALTER TABLE goal_run_action_outbox DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS goal_run_actions_super_admin_bypass ON goal_run_actions;
DROP POLICY IF EXISTS goal_run_actions_tenant_isolation ON goal_run_actions;
ALTER TABLE goal_run_actions DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS goal_run_steps_super_admin_bypass ON goal_run_steps;
DROP POLICY IF EXISTS goal_run_steps_tenant_isolation ON goal_run_steps;
ALTER TABLE goal_run_steps DISABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS goal_runs_super_admin_bypass ON goal_runs;
DROP POLICY IF EXISTS goal_runs_tenant_isolation ON goal_runs;
ALTER TABLE goal_runs DISABLE ROW LEVEL SECURITY;

DROP INDEX IF EXISTS idx_goal_run_outbox_lease;
DROP INDEX IF EXISTS idx_goal_run_outbox_retry;
DROP INDEX IF EXISTS idx_goal_run_actions_lease;
DROP INDEX IF EXISTS idx_goal_run_actions_retry;
DROP INDEX IF EXISTS idx_goal_run_steps_tenant_request;
DROP INDEX IF EXISTS idx_goal_runs_deadline;
DROP INDEX IF EXISTS idx_goal_runs_lease;
DROP INDEX IF EXISTS idx_goal_runs_tenant_status;

DROP TABLE IF EXISTS goal_run_action_outbox;
DROP TABLE IF EXISTS goal_run_actions;
DROP TABLE IF EXISTS goal_run_steps;
DROP TABLE IF EXISTS goal_runs;

COMMIT;
