-- Fix gpt-6 series models modality from text to multimodal
-- These models support image input according to OpenAI's specification,
-- similar to gpt-4o and gpt-5 series.
--
-- ★ 已被取代（2026-11-14 审计）：本脚本只覆盖 gpt-6 一族；全族修复以
-- scripts/fix-multimodal-models-comprehensive.sql 为准，官方可复现路径是
-- cmd/tools/backfill-modality（按 modelname.InferModality 当前规则回填，
-- 只升级、尊重 modality_source 闸门、幂等）。本脚本保留仅作历史记录，
-- 不要在新环境重放本脚本——新发现的 gpt-6* 行由写侧规则
-- （modelname/modality_defaults.go 的 {"gpt-6","multimodal"} 前缀）正确种值。

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
