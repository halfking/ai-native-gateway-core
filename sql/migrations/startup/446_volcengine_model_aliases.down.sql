-- Rollback for 446_volcengine_model_aliases.sql.
-- Only remove the aliases introduced by migration 446. The catalog manifest
-- requires a separately captured backup before it can be restored.

\set ON_ERROR_STOP on
BEGIN;

DELETE FROM public.model_aliases
WHERE raw_name IN (
    'doubao-seed-code',
    'doubao-seed-2.0-code',
    'glm-5.1',
    'deepseek-v4-pro',
    'deepseek-v4-flash'
)
AND canonical_id IN (
    SELECT id
    FROM public.models_canonical
    WHERE canonical_name IN (
        'doubao-seed-2-0-code-preview-260215',
        'glm-5-2-260617',
        'deepseek-v4-pro-260425',
        'deepseek-v4-flash-260425'
    )
);

COMMIT;
