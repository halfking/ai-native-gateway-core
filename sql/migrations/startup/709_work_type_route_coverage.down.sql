-- Down migration for 706_work_type_route_coverage.sql.
-- Removes only the rows 706 seeds; operator-added routes/config rows for the
-- same keys are deliberately preserved (706 never touched them either).
-- The schema_migrations ledger row is kept (append-only, 703 down convention).

BEGIN;

DELETE FROM work_type_model_route
WHERE work_type_key IN ('fn_call', 'code_audit', 'intent_classification', 'planning')
  AND canonical_name IN ('deepseek-v4-flash', 'minimax-m2.7', 'glm-5.2')
  AND tier IN ('primary', 'secondary')
  AND weight IN (1.00, 0.85, 0.80)
  AND min_score = 0;

DELETE FROM work_type_config
WHERE key IN ('code_audit', 'intent_classification', 'planning')
  AND synced_from_acc_at IS NULL;

COMMIT;
