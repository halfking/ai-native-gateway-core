-- 954_claude_opus_5_5_fix.sql
-- 修复 claude-opus-5-5 模型配置，确保模型可被正确访问和路由
-- 背景：2026-11-09 用户报告通过 llm.itestu.cn 访问 claude-opus-5-5 时总是错误

DO $$ 
BEGIN
    -- 1. 确保 models_canonical 中存在 claude-opus-5-5
    INSERT INTO models_canonical (
        canonical_name,
        display_name,
        family,
        modality,
        status,
        source
    ) VALUES (
        'claude-opus-5-5',
        'Claude Opus 5.5',
        'anthropic-claude',
        'text',
        'active',
        'manual'
    )
    ON CONFLICT (canonical_name) DO UPDATE SET
        display_name = EXCLUDED.display_name,
        status = 'active',
        updated_at = NOW();
    
    RAISE NOTICE 'Ensured claude-opus-5-5 exists in models_canonical';
    
    -- 2. 确保别名映射存在
    WITH canonical AS (
        SELECT id FROM models_canonical WHERE canonical_name = 'claude-opus-5-5'
    )
    INSERT INTO model_aliases (canonical_id, raw_name, status)
    SELECT canonical.id, alias, 'active'
    FROM canonical
    CROSS JOIN (VALUES 
        ('claude-opus-5-5'),
        ('claude_opus_5_5'),
        ('claude-opus-5.5'),
        ('claude_opus_5.5')
    ) AS t(alias)
    ON CONFLICT (canonical_id, raw_name) DO UPDATE SET
        status = 'active',
        updated_at = NOW();
    
    RAISE NOTICE 'Ensured claude-opus-5-5 aliases exist';
    
    -- 3. 如果 models_canonical 有 featured 列，标记为 featured
    IF EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_schema = 'public' 
          AND table_name = 'models_canonical' 
          AND column_name = 'featured'
    ) THEN
        UPDATE models_canonical 
        SET featured = TRUE 
        WHERE canonical_name = 'claude-opus-5-5';
        
        RAISE NOTICE 'Marked claude-opus-5-5 as featured';
    END IF;
    
    -- 4. 确保在 routing_policy 的 featured_models 中
    UPDATE routing_policy
    SET featured_models = array_append(
        COALESCE(featured_models, ARRAY[]::text[]),
        'claude-opus-5-5'
    )
    WHERE NOT ('claude-opus-5-5' = ANY(COALESCE(featured_models, ARRAY[]::text[])));
    
    RAISE NOTICE 'Added claude-opus-5-5 to routing_policy featured_models';
    
END $$;
