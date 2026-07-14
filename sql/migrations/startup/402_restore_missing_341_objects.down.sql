-- Migration 402: 回滚 - 移除恢复的 341 对象
--
-- 注意: 这些对象来自 migration 341，回滚此 migration 不会删除它们，
-- 因为它们应该已经存在（除非意外丢失）。
-- 此 down migration 仅用于测试和开发环境的清理。

-- 1) 删除触发器
DROP TRIGGER IF EXISTS model_offers_insert ON model_offers;
DROP TRIGGER IF EXISTS model_offers_update ON model_offers;
DROP TRIGGER IF EXISTS model_offers_delete ON model_offers;

-- 2) 删除函数
DROP FUNCTION IF EXISTS system_health_status(integer);

-- 3) 删除表（注意：这会丢失 node_probe_state 的所有状态数据）
DROP TABLE IF EXISTS node_probe_state;

-- 验证
DO $$
BEGIN
    RAISE NOTICE 'Migration 402 rollback completed: removed system_health_status(), node_probe_state, and 3 model_offers triggers';
END $$;
