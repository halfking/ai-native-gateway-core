-- ====================================================================
-- 客户端 API Keys - 用于压力测试的多客户端场景
-- ====================================================================
-- 创建测试应用和对应的 API keys
-- 每个客户端会有独立的会话管理和流量统计
-- ====================================================================

BEGIN;

-- 清理旧的测试数据
DELETE FROM public.api_keys WHERE id BETWEEN 8000 AND 8099;
DELETE FROM public.applications WHERE id BETWEEN 8000 AND 8099;

-- 创建 10 个测试应用
INSERT INTO public.applications (
    id, tenant_id, code, display_name, owner_user, enabled
) VALUES
    (8001, 'default', 'stress-test-app-01', 'Stress Test App 01', 'test-user', true),
    (8002, 'default', 'stress-test-app-02', 'Stress Test App 02', 'test-user', true),
    (8003, 'default', 'stress-test-app-03', 'Stress Test App 03', 'test-user', true),
    (8004, 'default', 'stress-test-app-04', 'Stress Test App 04', 'test-user', true),
    (8005, 'default', 'stress-test-app-05', 'Stress Test App 05', 'test-user', true),
    (8006, 'default', 'stress-test-app-06', 'Stress Test App 06', 'test-user', true),
    (8007, 'default', 'stress-test-app-07', 'Stress Test App 07', 'test-user', true),
    (8008, 'default', 'stress-test-app-08', 'Stress Test App 08', 'test-user', true),
    (8009, 'default', 'stress-test-app-09', 'Stress Test App 09', 'test-user', true),
    (8010, 'default', 'stress-test-app-10', 'Stress Test App 10', 'test-user', true);

-- 为每个应用创建 API key
-- 使用简单的测试密钥格式: sk-stress-test-0X
INSERT INTO public.api_keys (
    id, application_id, tenant_id, key_hash, key_prefix,
    status, enabled, rate_limit_rpm, key_tier
) VALUES
    (8001, 8001, 'default', 
     'sk-stress-test-01-hash-0000000000000000000000000000000000000001',
     'sk-stress-test-01',
     'active', true, 10000, 'default'),
    
    (8002, 8002, 'default',
     'sk-stress-test-02-hash-0000000000000000000000000000000000000002',
     'sk-stress-test-02',
     'active', true, 10000, 'default'),
    
    (8003, 8003, 'default',
     'sk-stress-test-03-hash-0000000000000000000000000000000000000003',
     'sk-stress-test-03',
     'active', true, 10000, 'default'),
    
    (8004, 8004, 'default',
     'sk-stress-test-04-hash-0000000000000000000000000000000000000004',
     'sk-stress-test-04',
     'active', true, 10000, 'default'),
    
    (8005, 8005, 'default',
     'sk-stress-test-05-hash-0000000000000000000000000000000000000005',
     'sk-stress-test-05',
     'active', true, 10000, 'default'),
    
    (8006, 8006, 'default',
     'sk-stress-test-06-hash-0000000000000000000000000000000000000006',
     'sk-stress-test-06',
     'active', true, 10000, 'default'),
    
    (8007, 8007, 'default',
     'sk-stress-test-07-hash-0000000000000000000000000000000000000007',
     'sk-stress-test-07',
     'active', true, 10000, 'default'),
    
    (8008, 8008, 'default',
     'sk-stress-test-08-hash-0000000000000000000000000000000000000008',
     'sk-stress-test-08',
     'active', true, 10000, 'default'),
    
    (8009, 8009, 'default',
     'sk-stress-test-09-hash-0000000000000000000000000000000000000009',
     'sk-stress-test-09',
     'active', true, 10000, 'default'),
    
    (8010, 8010, 'default',
     'sk-stress-test-10-hash-0000000000000000000000000000000000000010',
     'sk-stress-test-10',
     'active', true, 10000, 'default');

COMMIT;

-- 验证
SELECT 
    app.id,
    app.display_name,
    ak.key_prefix,
    ak.status,
    ak.rate_limit_rpm
FROM applications app
JOIN api_keys ak ON ak.application_id = app.id
WHERE app.id BETWEEN 8001 AND 8010
ORDER BY app.id;

SELECT '✓ 创建了 ' || COUNT(*)::TEXT || ' 个测试应用和 API keys' AS result
FROM applications
WHERE id BETWEEN 8001 AND 8010;
