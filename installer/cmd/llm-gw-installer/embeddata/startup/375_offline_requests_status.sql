-- 375_offline_requests_status.sql
-- 为 offline_activation_requests 补齐 status 和 reject_reason 字段
--
-- 修复 P0: store_pgx.go RejectOfflineRequest() 使用了这两个字段，
-- 但 374 迁移文件中漏掉了。

ALTER TABLE offline_activation_requests
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';

ALTER TABLE offline_activation_requests
    ADD COLUMN IF NOT EXISTS reject_reason TEXT;

CREATE INDEX IF NOT EXISTS idx_oar_status ON offline_activation_requests (status);
