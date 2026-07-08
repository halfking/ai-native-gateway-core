-- ====================================================================
-- Comprehensive Stress Test Credentials
-- 60+ mock providers, 15 custom models, multiple client API keys
-- ====================================================================
-- 目标：高压力全场景测试
-- - 60 个 mock 供应商 (provider_id 9010-9069, ports 19080-19139)
-- - 15 个自定义模型（避免与生产模型冲突）
-- - 每个供应商支持多个模型
-- - 多个客户端 API key 用于会话测试
-- ====================================================================

BEGIN;

-- 清理旧数据
DELETE FROM public.credential_model_bindings WHERE credential_id BETWEEN 9010 AND 9099;
DELETE FROM public.provider_models WHERE provider_id BETWEEN 9010 AND 9099;
DELETE FROM public.credentials WHERE id BETWEEN 9010 AND 9099;
DELETE FROM public.providers WHERE id BETWEEN 9010 AND 9099;

-- ====================================================================
-- 15 个自定义测试模型（不与生产模型冲突）
-- ====================================================================
-- 命名规则: loadtest-{category}-{variant}
-- 分类: mini, standard, pro, ultra, vision
-- 每类 3 个变体，确保足够的路由选择空间
-- ====================================================================

-- ====================================================================
-- 60 个 Mock 供应商 (provider_id 9010-9069)
-- ====================================================================
-- 端口范围: 19080-19139
-- 每个供应商支持多个模型（模拟真实供应商的多模型支持）
-- ====================================================================

\set encryption_key 'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA'

DO $$
DECLARE
    provider_id_val INT;
    credential_id_val INT;
    provider_model_id_val INT;
    binding_id_val INT;
    port_val INT;
    mock_token TEXT;
    provider_code TEXT;
    provider_label TEXT;
    base_url TEXT;
    
    -- 15 个自定义模型
    model_names TEXT[] := ARRAY[
        'loadtest-mini-alpha',
        'loadtest-mini-beta', 
        'loadtest-mini-gamma',
        'loadtest-standard-alpha',
        'loadtest-standard-beta',
        'loadtest-standard-gamma',
        'loadtest-pro-alpha',
        'loadtest-pro-beta',
        'loadtest-pro-gamma',
        'loadtest-ultra-alpha',
        'loadtest-ultra-beta',
        'loadtest-ultra-gamma',
        'loadtest-vision-alpha',
        'loadtest-vision-beta',
        'loadtest-vision-gamma'
    ];
    
    model_name TEXT;
    model_idx INT;
    models_per_provider INT;
    i INT;
BEGIN
    -- 循环创建 60 个供应商
    FOR i IN 0..59 LOOP
        provider_id_val := 9010 + i;
        credential_id_val := 9010 + i;
        port_val := 19080 + i;
        mock_token := 'mock-' || LPAD(i::TEXT, 2, '0');
        provider_code := 'loadtest-mock-' || LPAD(i::TEXT, 2, '0');
        provider_label := 'Loadtest Mock ' || LPAD(i::TEXT, 2, '0');
        base_url := 'http://localhost:' || port_val;
        
        -- 插入 provider
        INSERT INTO public.providers (
            id, tenant_id, code, display_name, kind, category, protocol,
            base_url, enabled, manual_disabled
        ) VALUES (
            provider_id_val, 'default', provider_code, provider_label,
            'cloud', 'official', 'openai-completions',
            base_url, true, false
        );
        
        -- 每个供应商支持 3-5 个模型（随机分布，模拟真实场景）
        models_per_provider := 3 + (i % 3);  -- 3, 4, or 5 models per provider
        
        -- 为该供应商添加模型
        FOR model_idx IN 0..(models_per_provider - 1) LOOP
            provider_model_id_val := 9010 + (i * 10) + model_idx;
            model_name := model_names[1 + ((i + model_idx) % 15)];  -- 循环使用15个模型
            
            INSERT INTO public.provider_models (
                id, provider_id, raw_model_name, outbound_model_name
            ) VALUES (
                provider_model_id_val, provider_id_val, model_name, model_name
            );
        END LOOP;
        
        -- 插入 credential (使用统一的加密key)
        INSERT INTO public.credentials (
            id, provider_id, tenant_id, label, secret_ciphertext,
            status, lifecycle_status, availability_state,
            quota_state, circuit_state, manual_disabled,
            fp_slot_limit, concurrency_limit
        ) VALUES (
            credential_id_val, provider_id_val, 'default', 
            provider_label || ' Key',
            'v1:legacy:VxvBl1KKTBfUKzGiwytI_l6pwl95wtFntgIiozcwYOoMazFjwQlQkkkPSb_aEpHZT9cWHWC-cbA',
            'active', 'active', 'ready',
            'ok', 'closed', false, 10, 20
        );
        
        -- 绑定 credential 和 models
        FOR model_idx IN 0..(models_per_provider - 1) LOOP
            provider_model_id_val := 9010 + (i * 10) + model_idx;
            binding_id_val := 9010 + (i * 10) + model_idx;
            
            INSERT INTO public.credential_model_bindings (
                id, credential_id, provider_model_id, available
            ) VALUES (
                binding_id_val, credential_id_val, provider_model_id_val, true
            );
        END LOOP;
        
    END LOOP;
    
    RAISE NOTICE '✓ 创建了 60 个 mock 供应商 (ID 9010-9069)';
    RAISE NOTICE '✓ 创建了 60 个 credentials';
    RAISE NOTICE '✓ 使用 15 个自定义模型: %', array_to_string(model_names, ', ');
END $$;

COMMIT;

-- ====================================================================
-- 验证注入结果
-- ====================================================================
SELECT 
    '供应商总数' AS metric,
    COUNT(*)::TEXT AS value
FROM providers 
WHERE id BETWEEN 9010 AND 9069

UNION ALL

SELECT 
    'Credentials 总数' AS metric,
    COUNT(*)::TEXT AS value
FROM credentials 
WHERE id BETWEEN 9010 AND 9069

UNION ALL

SELECT 
    '模型绑定总数' AS metric,
    COUNT(*)::TEXT AS value
FROM credential_model_bindings 
WHERE credential_id BETWEEN 9010 AND 9069

UNION ALL

SELECT 
    '可路由的 credentials' AS metric,
    COUNT(DISTINCT credential_id)::TEXT AS value
FROM v_routable_credential_models 
WHERE credential_id BETWEEN 9010 AND 9069;

-- 显示模型分布
SELECT 
    raw_model_name,
    COUNT(DISTINCT provider_id) AS provider_count,
    COUNT(*) AS total_bindings
FROM provider_models
WHERE provider_id BETWEEN 9010 AND 9069
GROUP BY raw_model_name
ORDER BY raw_model_name;
