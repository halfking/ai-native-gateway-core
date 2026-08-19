-- 362_standard_binding_placeholders.down.sql
-- Revert placeholder bindings created by 362.
-- Only deletes rows tagged credential_id=0 AND unavailable_reason='placeholder_pending_credential'
-- AND created within the last 30 days (age guard).
-- Onboarding flow flips credential_id to a real id and NULLs unavailable_reason, so real
-- bindings are protected. The age guard further shields against any onboarding script
-- that forgets to clear the reason within a 30-day window.
--
-- 审计修复（2026-08-20）：加入 30 天年龄保护，避免误清升级后的真实绑定。

BEGIN;

DELETE FROM credential_model_bindings
WHERE credential_id = 0
  AND unavailable_reason = 'placeholder_pending_credential'
  AND created_at > NOW() - INTERVAL '30 days';

COMMIT;