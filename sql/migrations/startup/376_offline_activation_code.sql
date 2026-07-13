-- 376_offline_activation_code.sql
-- 离线激活审批后生成的人类可读激活码（供客户核对 / 审计）

ALTER TABLE offline_activation_requests
    ADD COLUMN IF NOT EXISTS activation_code TEXT;

CREATE INDEX IF NOT EXISTS idx_oar_activation_code ON offline_activation_requests (activation_code)
    WHERE activation_code IS NOT NULL;
