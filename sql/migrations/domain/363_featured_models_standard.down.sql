-- 363_featured_models_standard.down.sql
-- Remove the 5 standard family IDs added by 363.
-- 仅移除由 363 写入的 name；若运维单独手动添加过同名（即便 363 之前已存在），也会被移除。
-- 这是 rollout 的对称行为；运维可在 down 前手工 snapshot routing_policy.featured_models。
--
-- 审计修复（2026-08-20）：保持与 363 一致的 per-name 行为；不做 snapshot。

BEGIN;

UPDATE routing_policy
SET featured_models = (
    SELECT COALESCE(array_agg(m) FILTER (WHERE m NOT IN (
        'grok-4.6', 'kimi-k3', 'kimi-k2.6',
        'gemini-3.5-flash', 'gemini-3-flash-preview'
    )), ARRAY[]::text[])
    FROM unnest(featured_models) AS m
),
updated_at = NOW()
WHERE id = 1;

COMMIT;