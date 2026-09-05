-- 362_standard_binding_placeholders.down.sql
-- Revert only placeholder bindings created by 362 that were not activated.
-- Activated bindings move to a real credential and clear the migration marker.

BEGIN;

DELETE FROM credential_model_bindings cmb
USING provider_models pm, providers p
WHERE cmb.provider_model_id = pm.id
  AND pm.provider_id = p.id
  AND pm.tenant_id = p.tenant_id
  AND cmb.credential_id = 0
  AND cmb.unavailable_reason = 'placeholder_pending_credential'
  AND cmb.plan_meta->>'source' = 'migration-362'
  AND p.tenant_id = 'default'
  AND p.code IN ('xai', 'moonshot', 'google-gemini')
  AND (
      (p.code = 'xai' AND pm.raw_model_name = 'grok-4.6')
      OR (p.code = 'moonshot' AND pm.raw_model_name IN (
          'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed'
      ))
      OR (p.code = 'google-gemini' AND pm.raw_model_name LIKE 'gemini-3%')
  );

COMMIT;
