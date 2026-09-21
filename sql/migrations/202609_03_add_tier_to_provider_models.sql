-- Migration: Add Tier Classification to Provider Models
-- Version: V3 Phase 1
-- Date: 2026-09-02
-- Purpose: Tag models with tier classification for filtering during recommendation
-- Related: AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 3.1, 7.3

BEGIN;

-- Add tier field to provider_models table
ALTER TABLE provider_models 
    ADD COLUMN IF NOT EXISTS tier TEXT CHECK (tier IN ('tier-a', 'tier-b', 'tier-c'));

-- Create index for tier-based filtering
CREATE INDEX IF NOT EXISTS idx_provider_models_tier 
    ON provider_models(tier) 
    WHERE tier IS NOT NULL;

-- Composite index for tier + availability filtering (common query pattern)
CREATE INDEX IF NOT EXISTS idx_provider_models_tier_available
    ON provider_models(tier, available)
    WHERE tier IS NOT NULL;

-- Add column comment
COMMENT ON COLUMN provider_models.tier IS 
    'Model tier classification: tier-a (high-perf, $15-50/1M), tier-b (standard, $5-15/1M), tier-c (economy, $0.5-5/1M)';

-- Classify existing models based on pricing and capabilities
-- Adjust canonical_name values based on actual data in your provider_models table

-- Tier-A: High-Performance Models ($15-50 per 1M tokens)
-- Deep reasoning, multi-file understanding, novel problem-solving
UPDATE provider_models 
SET tier = 'tier-a' 
WHERE canonical_name IN (
    'claude-opus-5',
    'claude-sonnet-4',
    'gpt-5.6-sol',
    'gpt-o1',
    'glm-5.3',
    'kimi-m3',
    'deepseek-v4-reasoning'
)
AND tier IS NULL;

-- Tier-B: Standard Models ($5-15 per 1M tokens)
-- Balanced speed/quality, good instruction following, single-file mastery
UPDATE provider_models 
SET tier = 'tier-b' 
WHERE canonical_name IN (
    'claude-opus-4.8',
    'claude-sonnet-3.7',
    'gpt-4.9',
    'gpt-4o',
    'glm-5.2',
    'glm-4-plus',
    'deepseek-v4-pro',
    'minimax-m3',
    'qwen-max',
    'yi-lightning'
)
AND tier IS NULL;

-- Tier-C: Economy Models ($0.5-5 per 1M tokens)
-- Fast response, template-based work, high throughput
UPDATE provider_models 
SET tier = 'tier-c' 
WHERE canonical_name IN (
    'minimax-m2.7',
    'glm-5.2-flash',
    'glm-4-flash',
    'deepseek-v4-flash',
    'deepseek-chat',
    'gpt-4o-mini',
    'claude-haiku-3.5',
    'qwen-turbo',
    'yi-medium',
    'local/minimax-m3'
)
AND tier IS NULL;

-- Default unclassified models to tier-b (safe fallback)
UPDATE provider_models 
SET tier = 'tier-b'
WHERE tier IS NULL 
  AND available = TRUE;

-- Verify the migration
DO $$
DECLARE
    tier_rec RECORD;
    v_unclassified INTEGER;
BEGIN
    RAISE NOTICE 'Migration verification:';
    RAISE NOTICE 'Tier distribution:';
    
    FOR tier_rec IN 
        SELECT 
            COALESCE(tier, 'unclassified') as tier,
            COUNT(*) as count,
            ARRAY_AGG(canonical_name ORDER BY canonical_name) as models
        FROM provider_models
        GROUP BY tier
        ORDER BY tier NULLS LAST
    LOOP
        RAISE NOTICE '  - %: % models', tier_rec.tier, tier_rec.count;
        
        -- Show first 5 models in each tier
        IF tier_rec.count <= 5 THEN
            RAISE NOTICE '    Models: %', tier_rec.models;
        ELSE
            RAISE NOTICE '    Sample: % ... (+ % more)', 
                tier_rec.models[1:5], 
                tier_rec.count - 5;
        END IF;
    END LOOP;
    
    -- Check for unclassified available models
    SELECT COUNT(*) INTO v_unclassified
    FROM provider_models
    WHERE tier IS NULL AND available = TRUE;
    
    IF v_unclassified > 0 THEN
        RAISE WARNING 'Found % available models without tier classification', v_unclassified;
    ELSE
        RAISE NOTICE 'All available models have tier classification ✓';
    END IF;
END $$;

COMMIT;

-- Expected output:
-- tier-a: 3-7 models (high-performance)
-- tier-b: 8-15 models (standard)
-- tier-c: 8-12 models (economy)
