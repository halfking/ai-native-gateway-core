-- 2026_06_12_pricing_audit_fixes.sql
-- Post-audit fixes for billing_mode consistency + plan_meta completeness
-- Date: 2026-06-12
-- Audit findings: 4 issues found, all fixed below

BEGIN;

-- Fix #1: xiaomi 5 offers billing_mode should be token_plan (not per_token)
-- Root cause: Go API /model_offers trigger + DDL backfill overwrote psql-set values
-- Affected: credential_id=9, offer ids 34,37,45,46,47 (gpt-4-turbo, o1, o3, o3-mini, o4-mini)
UPDATE credential_model_bindings
SET billing_mode = 'token_plan',
    pricing_updated_at = now(),
    updated_at = now()
WHERE id IN (34, 37, 45, 46, 47)
  AND billing_mode != 'token_plan';

-- Fix #2: demo-tokenplan 4 offers missing plan_meta
-- Affected: credential_id=11, offer ids 43,44,57,58 (volcano nemotron/nemoguard models)
UPDATE credential_model_bindings
SET plan_meta = jsonb_set(
  COALESCE(plan_meta, '{}'::jsonb),
  '{note}',
  '"volcano token-plan (demo credential)"'
)
WHERE id IN (43, 44, 57, 58)
  AND (plan_meta IS NULL OR plan_meta = '{}'::jsonb);

-- Fix #3: DDL backfill now excludes token_plan to prevent future overwrites
-- (This is a no-op if already correct, safety net for re-runs)
-- The actual fix is in 2026_06_12_pricing_billing_mode_meta.sql line 27

COMMIT;

-- Verification
SELECT 'audit_fix_result' AS check_name,
  (SELECT count(*) FROM credential_model_bindings WHERE billing_mode = 'token_plan') AS token_plan_count,
  (SELECT count(*) FROM credential_model_bindings WHERE billing_mode = 'per_token') AS per_token_legacy,
  (SELECT count(*) FROM credential_model_bindings WHERE billing_mode = 'token') AS token_count,
  (SELECT count(*) FROM credential_model_bindings WHERE plan_meta = '{}'::jsonb AND billing_mode = 'token_plan') AS token_plan_missing_meta;
