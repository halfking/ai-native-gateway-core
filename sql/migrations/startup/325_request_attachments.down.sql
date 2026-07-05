-- Rollback for Migration 325: 移除 request_logs.attachments 字段
-- 注意：此操作只删除元数据，不删除已写入文件系统的附件实体文件。
--       如需清理文件系统中的附件，请手动操作 LLM_GATEWAY_ATTACHMENT_DIR 目录。

DROP INDEX IF EXISTS idx_request_logs_has_attachments;
ALTER TABLE public.request_logs DROP COLUMN IF EXISTS attachments;
