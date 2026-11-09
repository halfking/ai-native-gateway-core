-- 954_claude_opus_5_5_fix.down.sql
-- 回滚 claude-opus-5-5 配置修复

DO $$ 
BEGIN
    -- 从 routing_policy 中移除
    UPDATE routing_policy
    SET featured_models = array_remove(featured_models, 'claude-opus-5-5')
    WHERE 'claude-opus-5-5' = ANY(featured_models);
    
    -- 注意：不删除 models_canonical 和 model_aliases 中的记录
    -- 因为可能有实际的请求历史和绑定依赖这些记录
    
    RAISE NOTICE 'Removed claude-opus-5-5 from routing_policy featured_models';
    
END $$;
