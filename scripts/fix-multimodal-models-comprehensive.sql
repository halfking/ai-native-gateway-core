-- Comprehensive fix for multimodal models incorrectly marked as text
-- This script updates models from gpt-4o, gpt-5, gpt-6, claude-, and gemini- series
-- that should support multimodal input according to vendor specifications.
--
-- Background: The inferModalityFromName function in catalog/display.go uses prefix
-- matching (gpt-4o, gpt-5, gpt-6, claude-, gemini-) to identify multimodal models,
-- but some models in the database were incorrectly marked as 'text'.
--
-- Affected model families:
-- - gpt-4o series: All variants support vision
-- - gpt-5 series: All variants support vision (except pure embedding models)
-- - gpt-6 series: All variants support vision
-- - claude- series: Claude 3+ models support vision
-- - gemini- series: Most models support vision
--
-- Date: 2026-11-13
-- Related: 161e29507 (code fix for gpt-6)

BEGIN;

-- Display current state before update
\echo '=== Current state of models to be updated ==='
SELECT 
    COUNT(*) as total_models,
    COUNT(CASE WHEN canonical_name LIKE 'gpt-4o%' THEN 1 END) as gpt4o_count,
    COUNT(CASE WHEN canonical_name LIKE 'gpt-5%' THEN 1 END) as gpt5_count,
    COUNT(CASE WHEN canonical_name LIKE 'gpt-6%' THEN 1 END) as gpt6_count,
    COUNT(CASE WHEN canonical_name LIKE 'claude-%' THEN 1 END) as claude_count,
    COUNT(CASE WHEN canonical_name LIKE 'gemini-%' THEN 1 END) as gemini_count
FROM models_canonical
WHERE 
    modality = 'text'
    AND modality_source = 'inferred'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    );

\echo ''
\echo '=== Models to be updated ==='
SELECT 
    id,
    canonical_name,
    modality,
    modality_source
FROM models_canonical
WHERE 
    modality = 'text'
    AND modality_source = 'inferred'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    )
ORDER BY canonical_name;

\echo ''
\echo '=== Performing update ==='

-- Update all affected models to multimodal
UPDATE models_canonical
SET 
    modality = 'multimodal',
    modality_source = 'inferred',
    updated_at = now()
WHERE 
    modality = 'text'
    AND modality_source = 'inferred'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    );

\echo ''
\echo '=== Update summary ==='
SELECT 
    COUNT(*) as updated_count
FROM models_canonical
WHERE 
    modality = 'multimodal'
    AND modality_source = 'inferred'
    AND updated_at > now() - interval '1 minute'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    );

\echo ''
\echo '=== Verification: Updated models ==='
SELECT 
    id,
    canonical_name,
    modality,
    modality_source,
    updated_at
FROM models_canonical
WHERE 
    modality = 'multimodal'
    AND modality_source = 'inferred'
    AND updated_at > now() - interval '1 minute'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    )
ORDER BY canonical_name;

\echo ''
\echo '=== Verification: Any remaining text models in these families ==='
SELECT 
    COUNT(*) as remaining_text_models
FROM models_canonical
WHERE 
    modality = 'text'
    AND modality_source = 'inferred'
    AND (
        canonical_name LIKE 'gpt-4o%'
        OR canonical_name LIKE 'gpt-5%'
        OR canonical_name LIKE 'gpt-6%'
        OR canonical_name LIKE 'claude-%'
        OR canonical_name LIKE 'gemini-%'
    );

COMMIT;

\echo ''
\echo '=== Update completed successfully ==='
\echo 'All gpt-4o, gpt-5, gpt-6, claude-, and gemini- series models'
\echo 'have been updated from text to multimodal where applicable.'
