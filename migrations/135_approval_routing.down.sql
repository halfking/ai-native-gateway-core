-- Rollback Migration 135: Approval Routing Rules & Notification Channels

-- 删除触发器与函数
DROP TRIGGER IF EXISTS trg_notification_channels_updated_at ON notification_channels;
DROP TRIGGER IF EXISTS trg_approval_routing_updated_at ON approval_routing_rules;
DROP FUNCTION IF EXISTS update_notification_channels_updated_at();
DROP FUNCTION IF EXISTS update_approval_routing_updated_at();

-- 删除表（按依赖顺序）
DROP TABLE IF EXISTS notification_send_log;
DROP TABLE IF EXISTS notification_channels;
DROP TABLE IF EXISTS approval_routing_rules;
