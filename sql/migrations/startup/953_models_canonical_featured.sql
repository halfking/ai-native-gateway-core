-- 953_models_canonical_featured.sql
-- 增加 models_canonical.featured 列，用于标记需要优先探测的模型
-- 背景：2026-11-09 恢复检测及时性优化 —— 免费/入门级模型需要更高的探测优先级
-- 以减少「上游已恢复但系统 ~24h 未感知」问题

DO $$ 
BEGIN
    -- Add featured column if not exists
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns 
        WHERE table_schema = 'public' 
          AND table_name = 'models_canonical' 
          AND column_name = 'featured'
    ) THEN
        ALTER TABLE models_canonical 
        ADD COLUMN featured BOOLEAN NOT NULL DEFAULT FALSE;
        
        RAISE NOTICE 'Added models_canonical.featured column';
    END IF;
    
    -- Create index if not exists
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes 
        WHERE schemaname = 'public' 
          AND tablename = 'models_canonical' 
          AND indexname = 'idx_models_canonical_featured'
    ) THEN
        CREATE INDEX idx_models_canonical_featured 
        ON models_canonical(featured) 
        WHERE featured = TRUE;
        
        RAISE NOTICE 'Created idx_models_canonical_featured index';
    END IF;
    
    -- Add column comment
    COMMENT ON COLUMN models_canonical.featured IS 
        'Whether this model should be prioritized for probing and monitoring. ' ||
        'Typically includes free-tier models and commonly-used entry models. ' ||
        'Featured models are probed more frequently to ensure timely recovery detection.';
    
    -- Mark initial featured models (free/entry tier models)
    -- These are common entry-level models that users rely on
    UPDATE models_canonical SET featured = TRUE 
    WHERE featured = FALSE
      AND status = 'active'
      AND canonical_name IN (
        -- Claude free tier
        'claude-haiku-3-5',
        'claude-3-haiku',
        'claude-3-5-haiku',
        'claude-haiku-4-5',
        'claude-haiku-5',
        -- OpenAI free/cheap tier  
        'gpt-4o-mini',
        'gpt-3.5-turbo',
        -- Gemini free tier
        'gemini-2-flash',
        'gemini-1.5-flash',
        'gemini-2.0-flash',
        -- Zhipu (GLM) free tier
        'glm-5-flash',
        'glm-5.1-flash',
        'glm-5.3-flash',
        -- DeepSeek free tier
        'deepseek-v3-flash',
        'deepseek-flash',
        'deepseek-v4-flash',
        'deepseek-v4.1-flash',
        -- MiniMax entry tier
        'minimax-abab7-lite',
        'minimax-abab7-flash',
        -- Qwen entry tier
        'qwen-turbo-flash',
        'qwen-plus-flash',
        -- Kimi entry tier
        'kimi-k3',
        -- Other common entry models
        'yi-lightning',
        'yi-spark',
        'moonshot-v1-8k',
        'baichuan2-turbo'
      );
    
    RAISE NOTICE 'Marked % models as featured', 
        (SELECT COUNT(*) FROM models_canonical WHERE featured = TRUE);
        
END $$;
