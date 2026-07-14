-- Migration 401: request_attachments 关系化表
-- Created: 2026-07-15
-- Purpose: 将 request_logs.attachments JSONB 字段升级为独立关系表，
--          支持按 hash、status、size 的索引查询、生命周期统计、批量清理。
--
-- 背景：
--   Migration 325 在 request_logs 上加了 attachments JSONB 列，存每个请求
--   的附件元数据数组。JSONB 在以下场景受限：
--     1. 无法按 hash/status 索引（如"过去 7 天 store_failed 的附件数"）
--     2. 无法做批量清理（"清理 30 天前的 stored 附件记录"）
--     3. 无法对外暴露 admin 端点（"某 hash 被多少请求引用过"）
--     4. 整体写入放大：JSONB 数组越大，每次 INSERT 越大
--
-- 本迁移新增独立表 request_attachments，与 request_logs.attachments JSONB
-- 双写（向后兼容），admin 端点逐步切换到关系表查询。
--
-- 字段对齐 attachments.AttachmentMetadata：
--   type, content_type, size, path, hash, original_url,
--   message_index, block_index, status, error_code, created_at
--
-- 索引：
--   1. (request_id) — JOIN request_logs
--   2. (hash) — dedup/引用统计
--   3. (status, created_at DESC) — 失败附件排查
--   4. (created_at) — 批量清理
--
-- Idempotent: CREATE TABLE IF NOT EXISTS / CREATE INDEX IF NOT EXISTS

CREATE TABLE IF NOT EXISTS public.request_attachments (
    id              BIGSERIAL PRIMARY KEY,
    request_id      TEXT NOT NULL,
    attachment_type TEXT NOT NULL DEFAULT 'image',
    content_type    TEXT,
    size_bytes      BIGINT NOT NULL DEFAULT 0,
    storage_path    TEXT,
    hash            TEXT,
    original_url    TEXT,
    message_index   INTEGER NOT NULL DEFAULT 0,
    block_index     INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'detected',
    error_code      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT request_attachments_status_check CHECK (
        status IN ('detected', 'storing', 'stored', 'manifest_ready',
                   'sent', 'store_failed')
    )
);

-- Index 1: JOIN request_logs by request_id
CREATE INDEX IF NOT EXISTS idx_request_attachments_request_id
    ON public.request_attachments (request_id);

-- Index 2: lookup by content hash (dedup, reuse statistics)
CREATE INDEX IF NOT EXISTS idx_request_attachments_hash
    ON public.request_attachments (hash)
    WHERE hash IS NOT NULL;

-- Index 3: failure / lifecycle scans
CREATE INDEX IF NOT EXISTS idx_request_attachments_status_time
    ON public.request_attachments (status, created_at DESC);

-- Index 4: bulk cleanup by age
CREATE INDEX IF NOT EXISTS idx_request_attachments_created_at
    ON public.request_attachments (created_at DESC);

COMMENT ON TABLE public.request_attachments IS
'Per-request attachment metadata extracted from inbound bodies (base64/data-URI).
Mirrors request_logs.attachments JSONB (migration 325) in relational form.
Attachment bytes are persisted by the gateway under
LLM_GATEWAY_ATTACHMENT_DIR (SHA256 two-level sharding from migration 58f31d74d);
this table stores metadata only.

Status lifecycle:
  detected      — Extractor found the attachment
  storing       — Storage.SaveBase64Image is running
  stored        — File is on disk and dedup-checked
  manifest_ready — Captured in failover manifest (reusable on retry)
  sent          — Forwarded to upstream provider
  store_failed  — Storage failure (strict mode → 503)

Migration 401 (2026-07-15) — Phase 2C relational upgrade.';