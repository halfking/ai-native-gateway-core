-- Fix gpt-6 series models modality from text to multimodal
-- These models support image input according to OpenAI's specification,
-- similar to gpt-4o and gpt-5 series.

BEGIN;

-- Update gpt-6 series models that are currently marked as text
UPDATE models_canonical
SET 
    modality = 'multimodal',
    modality_source = 'inferred',
    updated_at = now()
WHERE 
    canonical_name LIKE 'gpt-6%'
    AND modality = 'text'
    AND modality_source = 'inferred';

-- Verify the update
SELECT 
    id, 
    canonical_name, 
    modality, 
    modality_source,
    updated_at
FROM models_canonical
WHERE canonical_name LIKE 'gpt-6%'
ORDER BY canonical_name;

COMMIT;
