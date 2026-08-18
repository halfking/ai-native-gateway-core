-- 355_add_gemini_3_series.down.sql
-- Rollback Gemini 3 series models

BEGIN;

-- Remove model aliases
DELETE FROM model_aliases
WHERE canonical_id IN (
    SELECT id FROM models_canonical 
    WHERE canonical_name IN (
        'gemini-3.7-flash',
        'gemini-3.6-flash',
        'gemini-3.5-flash',
        'gemini-3.5-flash-lite',
        'gemini-3.1-flash-lite',
        'gemini-3.1-pro',
        'gemini-3.1-flash-image',
        'gemini-3-pro-image'
    )
);

-- Remove models from models_canonical
DELETE FROM models_canonical
WHERE canonical_name IN (
    'gemini-3.7-flash',
    'gemini-3.6-flash',
    'gemini-3.5-flash',
    'gemini-3.5-flash-lite',
    'gemini-3.1-flash-lite',
    'gemini-3.1-pro',
    'gemini-3.1-flash-image',
    'gemini-3-pro-image'
);

COMMIT;
