-- Migration 336: 清理重复的 provider_models 条目并修复损坏的 bindings
-- Date: 2026-07-10
-- Author: System Fix
--
-- Problem:
--   1. mimo-v2.5-pro 有多个 provider_model 条目指向同一个 canonical_id=100：
--      - id=6 (provider=1, raw='mimo-v2.5-pro', std='mimo-v2.5-pro') ✓ 正常
--      - id=7883 (provider=1, raw='MiMo-V2.5-Pro', std='mimo-v2.5-pro') ✗ 重复且损坏
--      其中 binding 7883 (credential=9, provider_model=7883) 标记为 available=false,
--      unavailable_reason='model_probe_broken'
--
--   2. 同一个 provider 对同一个 canonical model 有多个不同大小写的 raw_model_name
--      导致路由混乱和重复探测
--
-- Fix:
--   1. 删除损坏的 provider_model id=7883 及其 binding
--   2. 添加 UNIQUE 约束防止未来出现重复：
--      (provider_id, LOWER(standardized_name)) 必须唯一
--
-- Verification:
--   SELECT pm.id, pm.provider_id, pm.raw_model_name, pm.standardized_name, pm.canonical_id
--   FROM provider_models pm
--   WHERE pm.canonical_id = 100
--   ORDER BY pm.id;

BEGIN;

-- 1. 删除损坏的 binding (provider_model_id=7883)
DELETE FROM credential_model_bindings
WHERE provider_model_id = 7883;

-- 2. 删除损坏的 provider_model
DELETE FROM provider_models
WHERE id = 7883;

-- 3. 记录删除操作到审计表
DO $$
BEGIN
    BEGIN
        INSERT INTO runtask_errors (task_name, payload, created_at)
        VALUES (
            'mig_336_dedupe',
            jsonb_build_object(
                'deleted_provider_model_id', 7883,
                'deleted_binding_count', 1,
                'reason', 'duplicate MiMo-V2.5-Pro with model_probe_broken',
                'ran_at', NOW()
            ),
            NOW()
        );
    EXCEPTION WHEN OTHERS THEN
        NULL;  -- 审计表不存在则跳过
    END;
END $$;

-- 4. 添加 UNIQUE 约束防止未来出现同一 provider 对同一 standardized_name 的重复条目
-- 注意：standardized_name 是大小写规范化后的名称，所以用 LOWER() 确保真正去重
CREATE UNIQUE INDEX IF NOT EXISTS idx_provider_models_unique_std_name
ON provider_models (provider_id, LOWER(standardized_name));

COMMENT ON INDEX idx_provider_models_unique_std_name IS
  '防止同一 provider 对同一模型创建多个不同大小写的 raw_model_name 条目';

-- 5. 触发自动路由刷新
SELECT pg_notify('auto_route_refresh', 'manual:336_dedupe_provider_models');

COMMIT;
