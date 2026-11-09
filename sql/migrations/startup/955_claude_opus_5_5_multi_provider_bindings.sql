-- 955_claude_opus_5_5_multi_provider_bindings.sql
-- 为 claude-opus-5-5 添加多个 provider 绑定，实现冗余路由
-- 背景：2026-11-09 用户报告"没有可用节点"，发现只有1个provider绑定且该provider不可用
-- 修复：添加所有可用的 audit-local-provider 的绑定

DO $$ 
DECLARE
    v_canonical_id bigint;
    v_provider_record RECORD;
    v_credential_record RECORD;
    v_provider_model_id bigint;
    v_binding_count int := 0;
BEGIN
    -- 1. 获取 claude-opus-5-5 的 canonical_id
    SELECT id INTO v_canonical_id
    FROM models_canonical
    WHERE canonical_name = 'claude-opus-5-5';
    
    IF v_canonical_id IS NULL THEN
        RAISE NOTICE 'claude-opus-5-5 not found in models_canonical, skipping';
        RETURN;
    END IF;
    
    -- 2. 为所有可用的 audit-local-provider 添加 provider_models 和绑定
    FOR v_provider_record IN 
        SELECT DISTINCT p.id as provider_id, p.code
        FROM providers p
        JOIN credentials c ON c.provider_id = p.id
        WHERE p.code LIKE 'audit-local-provider-%'
          AND p.enabled = TRUE
          AND c.status = 'active'
          AND c.lifecycle_status = 'active'
    LOOP
        -- 创建或更新 provider_model
        INSERT INTO provider_models (
            provider_id,
            tenant_id,
            raw_model_name,
            canonical_id,
            standardized_name,
            canonical_raw_name,
            modality,
            source,
            available
        ) VALUES (
            v_provider_record.provider_id,
            'default',
            'claude-opus-5-5',
            v_canonical_id,
            'claude-opus-5-5',
            'claude-opus-5-5',
            'text',
            'manual',
            TRUE
        )
        ON CONFLICT (provider_id, raw_model_name) 
        DO UPDATE SET
            canonical_id = EXCLUDED.canonical_id,
            available = TRUE,
            updated_at = NOW()
        RETURNING id INTO v_provider_model_id;
        
        RAISE NOTICE 'Provider model created/updated: provider_id=%, provider_model_id=%', 
            v_provider_record.provider_id, v_provider_model_id;
        
        -- 为该 provider 的所有活跃凭据创建绑定
        FOR v_credential_record IN
            SELECT id
            FROM credentials
            WHERE provider_id = v_provider_record.provider_id
              AND status = 'active'
              AND lifecycle_status = 'active'
        LOOP
            INSERT INTO credential_model_bindings (
                credential_id,
                provider_model_id,
                available,
                routing_tier,
                weight
            ) VALUES (
                v_credential_record.id,
                v_provider_model_id,
                TRUE,
                2,
                100
            )
            ON CONFLICT (credential_id, provider_model_id)
            DO UPDATE SET
                available = TRUE,
                routing_tier = EXCLUDED.routing_tier,
                weight = EXCLUDED.weight,
                updated_at = NOW();
            
            v_binding_count := v_binding_count + 1;
        END LOOP;
    END LOOP;
    
    RAISE NOTICE 'Added % credential_model_bindings for claude-opus-5-5', v_binding_count;
    
END $$;
