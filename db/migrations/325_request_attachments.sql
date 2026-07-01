-- Migration 325: request_logs 增加附件元数据字段
-- Created: 2026-07-01
-- Purpose: 记录每个请求中携带的图片/文件附件元数据，便于在日志页面查看和下载。
--
-- 背景：
--   客户端请求中经常包含 base64 编码的图片（data:image/...;base64,...）或
--   其他附件。这些附件体积可能很大，且需要：
--   1. 持久化到文件系统以便后续查看/下载
--   2. 在 request_logs 中记录元数据（路径、类型、大小、hash）
--   3. 附件内容本身存文件系统，不存数据库（避免 request_logs 膨胀）
--
-- 本迁移只新增元数据 JSONB 字段，附件实体文件由网关写入配置的存储目录。
--
-- Idempotent: ADD COLUMN IF NOT EXISTS / CREATE INDEX IF NOT EXISTS

-- ■ 1. 新增 attachments JSONB 字段
ALTER TABLE public.request_logs
    ADD COLUMN IF NOT EXISTS attachments JSONB;

-- ■ 2. 索引：用于通过 request_id 快速查找某请求的附件
-- attachments 字段为数组，包含 type/content_type/size/path/hash/original_url 等元数据。
-- 大多数查询通过 request_id 主键即可定位行，此索引主要用于附件存在性过滤。
CREATE INDEX IF NOT EXISTS idx_request_logs_has_attachments
    ON public.request_logs (ts DESC)
    WHERE attachments IS NOT NULL;

-- ■ 3. 注释
COMMENT ON COLUMN public.request_logs.attachments IS
'附件元数据 JSONB 数组。每个元素格式：
 {
   "type": "image",                      -- image | file
   "content_type": "image/png",          -- MIME 类型
   "size": 12345,                        -- 字节数
   "path": "2026/07/req_xxx/abc123.png", -- 文件系统相对路径（相对存储根目录）
   "hash": "sha256hex...",               -- 内容 SHA256（用于去重校验）
   "original_url": "data:image/png...",  -- 原始引用（data URI 截断至200字符；HTTP URL 原样）
   "message_index": 0,                   -- 所在 message 的索引（便于定位）
   "created_at": "2026-07-01T..."
 }
 NULL 表示该请求无附件。附件实体文件由网关写入 LLM_GATEWAY_ATTACHMENT_DIR 目录，
 通过 GET /api/attachments/{path} 访问。';

-- ■ 4. 回滚脚本（325_request_attachments.down.sql）：
--       ALTER TABLE public.request_logs DROP COLUMN IF EXISTS attachments;
--       DROP INDEX IF EXISTS idx_request_logs_has_attachments;
