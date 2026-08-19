-- 361_standard_provider_models.down.sql
-- Remove provider_models rows pre-filled by 361.
-- 仅删除 source='migration-361' 的行；发现 worker（source='discovery' 或其他值）写入的行保留。
--
-- 审计修复（2026-08-20）：通过 providers.code 解析 provider_id；
-- 通过 source 列精确删除，避免误清。

BEGIN;

DELETE FROM provider_models pm
USING providers p
WHERE pm.provider_id = p.id
  AND pm.source = 'migration-361'
  AND p.tenant_id = 'default'
  AND p.code IN ('xai', 'moonshot', 'google-gemini')
  AND pm.raw_model_name IN (
    'grok-4.6',
    'kimi-k3', 'kimi-k2.6', 'kimi-k2.7-code', 'kimi-k2.7-code-highspeed',
    'gemini-3.6-flash', 'gemini-3.5-flash', 'gemini-3.5-flash-lite',
    'gemini-3.1-flash-lite', 'gemini-3.1-flash-lite-image', 'gemini-3.1-pro-preview',
    'gemini-3.1-flash-image', 'gemini-3-pro-image', 'gemini-3-flash-preview',
    'gemini-omni-flash'
  );

COMMIT;