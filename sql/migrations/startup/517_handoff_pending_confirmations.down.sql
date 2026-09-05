-- Down migration for 517_handoff_pending_confirmations.sql
-- 仅用于测试环境重建；生产回滚应先停止确认入口并保留审计日志。
BEGIN;

DROP TABLE IF EXISTS handoff_pending_confirmations;

COMMIT;
