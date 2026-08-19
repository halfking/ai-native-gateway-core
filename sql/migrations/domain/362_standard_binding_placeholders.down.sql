-- 362_standard_binding_placeholders.down.sql
-- Revert placeholder bindings created by 362.
-- Only deletes rows tagged unavailable_reason='placeholder_pending_credential'
-- (real bindings upgraded from placeholder via onboarding are preserved).

BEGIN;

DELETE FROM credential_model_bindings
WHERE credential_id = 0
  AND unavailable_reason = 'placeholder_pending_credential';

COMMIT;