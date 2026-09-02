-- Migration: Create Task Type Tier Configuration Table
-- Version: V3 Phase 1
-- Date: 2026-09-02
-- Purpose: Map task types to preferred model tiers with fallback options
-- Related: AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 2.1, 7.2

BEGIN;

-- Create task type tier configuration table
CREATE TABLE IF NOT EXISTS task_type_tier_config (
    id SERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,
    preferred_tier TEXT NOT NULL CHECK (preferred_tier IN ('tier-a', 'tier-b', 'tier-c')),
    fallback_tiers TEXT[] DEFAULT ARRAY[]::TEXT[],
    min_confidence DECIMAL(3,2) DEFAULT 0.70 CHECK (min_confidence >= 0 AND min_confidence <= 1),
    tenant_id TEXT, -- NULL = global default, specific value = tenant override
    enabled BOOLEAN DEFAULT TRUE,
    description TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(task_type, COALESCE(tenant_id, ''))
);

-- Create index for fast lookups
CREATE INDEX idx_task_type_tier_config_lookup 
    ON task_type_tier_config(task_type, tenant_id, enabled)
    WHERE enabled = TRUE;

-- Add table comment
COMMENT ON TABLE task_type_tier_config IS 
    'Maps task types to preferred model tiers with fallback options. Supports tenant-specific overrides.';

COMMENT ON COLUMN task_type_tier_config.task_type IS 
    'Task category: architecture, audit, debugging, coding, refactoring, testing, devops, documentation, summary, dependency';

COMMENT ON COLUMN task_type_tier_config.preferred_tier IS 
    'Primary tier for this task type: tier-a (high-perf), tier-b (standard), tier-c (economy)';

COMMENT ON COLUMN task_type_tier_config.fallback_tiers IS 
    'Array of fallback tiers to try if preferred tier has no available models. Order matters.';

COMMENT ON COLUMN task_type_tier_config.min_confidence IS 
    'Minimum classification confidence (0.0-1.0) required to use this tier. Low confidence uses tier-a.';

COMMENT ON COLUMN task_type_tier_config.tenant_id IS 
    'NULL = global default for all tenants. Specific tenant_id = tenant-specific override.';

COMMENT ON COLUMN task_type_tier_config.enabled IS 
    'FALSE disables this configuration (falls back to tier-b default)';

-- Insert default 10-category configuration
-- Based on AUTO_MODEL_OPTIMIZATION_V3_PLAN.md Section 2.1

INSERT INTO task_type_tier_config 
    (task_type, preferred_tier, fallback_tiers, min_confidence, description) 
VALUES
    -- Tier-A: High-Performance Tasks (Deep reasoning, complex problems)
    ('architecture', 'tier-a', ARRAY['tier-b'], 0.70, 
     'System design, API design, technical proposals, architecture reviews'),
    
    ('audit', 'tier-a', ARRAY['tier-b'], 0.70,
     'Code review, security audit, PR review, vulnerability analysis'),
    
    ('debugging', 'tier-a', ARRAY['tier-b'], 0.65,
     'Bug investigation, root cause analysis, stack trace debugging'),
    
    -- Tier-B: Standard Tasks (Balanced quality/cost)
    ('coding', 'tier-b', ARRAY['tier-a', 'tier-c'], 0.75,
     'Greenfield development, feature implementation, API integration'),
    
    ('refactoring', 'tier-b', ARRAY['tier-a', 'tier-c'], 0.70,
     'Code restructuring, optimization, clean-up'),
    
    ('testing', 'tier-b', ARRAY['tier-c'], 0.75,
     'Unit/integration test generation, test coverage'),
    
    -- Tier-C: Economy Tasks (Template-based, high throughput)
    ('devops', 'tier-c', ARRAY['tier-b'], 0.80,
     'CI/CD, deployment, infrastructure scripting, container config'),
    
    ('documentation', 'tier-c', ARRAY[]::TEXT[], 0.85,
     'Comments, README, API docs, inline documentation'),
    
    ('summary', 'tier-c', ARRAY[]::TEXT[], 0.85,
     'Code summarization, session recap, overview generation'),
    
    ('dependency', 'tier-c', ARRAY['tier-b'], 0.75,
     'Dependency analysis, upgrade planning, package management')
ON CONFLICT (task_type, COALESCE(tenant_id, '')) DO NOTHING;

-- Verify the migration
DO $$
DECLARE
    v_count INTEGER;
BEGIN
    SELECT COUNT(*) INTO v_count
    FROM task_type_tier_config
    WHERE tenant_id IS NULL;
    
    RAISE NOTICE 'Migration verification:';
    RAISE NOTICE '  - Global task type configurations created: %', v_count;
    
    IF v_count != 10 THEN
        RAISE WARNING 'Expected 10 task type configurations, found %', v_count;
    END IF;
    
    -- Show tier distribution
    RAISE NOTICE 'Tier distribution:';
    FOR v_count IN 
        SELECT preferred_tier, COUNT(*) as count
        FROM task_type_tier_config
        WHERE tenant_id IS NULL
        GROUP BY preferred_tier
        ORDER BY preferred_tier
    LOOP
        RAISE NOTICE '  - %', v_count;
    END LOOP;
END $$;

COMMIT;

-- Expected tier distribution:
-- tier-a: 3 task types (architecture, audit, debugging)
-- tier-b: 3 task types (coding, refactoring, testing)
-- tier-c: 4 task types (devops, documentation, summary, dependency)
