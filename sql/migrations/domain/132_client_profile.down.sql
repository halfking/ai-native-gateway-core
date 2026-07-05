-- Rollback Migration 132: Client Profile System

-- 删除触发器和函数
DROP TRIGGER IF EXISTS trg_client_profiles_updated_at ON client_profiles;
DROP FUNCTION IF EXISTS update_client_profile_updated_at();

-- 删除表（级联删除索引）
DROP TABLE IF EXISTS client_behavior_events;
DROP TABLE IF EXISTS client_profiles;
