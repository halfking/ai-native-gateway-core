-- 361_standard_provider_models.down.sql
-- Remove the standard provider_models rows pre-filled by 361.
-- Only deletes rows whose source=361 (created in this migration window);
-- rows created later by the discovery worker are preserved (last_seen_at > migration-361 cut).

BEGIN;

DELETE FROM provider_models pm
WHERE pm.provider_id IN (10, 17, 30)
  AND pm.tenant_id = 'default'
  AND pm.raw_model_name IN (
    'grok-4.6',
    'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed',
    'gemini-3.6-flash', 'gemini-3.5-flash', 'gemini-3.5-flash-lite',
    'gemini-3.1-flash-lite', 'gemini-3.1-flash-lite-image', 'gemini-3.1-pro-preview',
    'gemini-3.1-flash-image', 'gemini-3-pro-image', 'gemini-3-flash-preview',
    'gemini-omni-flash'
  );

COMMIT;