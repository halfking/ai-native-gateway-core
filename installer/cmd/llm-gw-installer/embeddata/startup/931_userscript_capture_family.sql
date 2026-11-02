-- ===========================================================================
-- File:          sql/migrations/startup/931_userscript_capture_family.sql
-- Migration:     931
-- Database:      llm_gateway
-- Purpose:       userscript capture 摄取表族（P0-3 M1，ADR
--                docs/adr/2026-11-02-userscript-capture-gateway-endpoint.md D5）。
--                三张表：capture_batches（批次头）/ capture_messages
--                （消息头 + body_ref）/ capture_bodies（正文，网关权威持有）。
--                RLS 沿用会话族惯例（tenant GUC）。
--
-- 幂等：capture_messages 的 (tenant_id, idempotency_key) 唯一约束是
--       重试去重收敛点；同批次重放命中唯一冲突即计 duplicate。
--
-- Down: 931_userscript_capture_family.down.sql
-- ===========================================================================

BEGIN;

-- ★ 锁守卫（§44 纪律，871+ 强锁 DDL 迁移必带）：本迁移 CREATE TABLE /
--   CREATE INDEX 拿强锁 ⇒ 等锁期间会堵住全库 ⇒ 拿不到锁就快速失败，
--   不排队堵写路径。★ SET LOCAL 只在事务块内生效 ⇒ 必须落在 BEGIN/COMMIT 里
--   （写在事务块外只是 WARNING、守卫是空文 —— 928 起的 SET LOCAL 有效性门）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

CREATE TABLE IF NOT EXISTS public.capture_batches (
    batch_id          UUID PRIMARY KEY,
    tenant_id         VARCHAR(255) NOT NULL,
    end_user_id       TEXT,
    source_system     TEXT NOT NULL,
    client_session_key TEXT NOT NULL,
    correlation_id    TEXT,
    schema_version    TEXT NOT NULL,
    message_count     INT NOT NULL,
    accepted_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS idx_capture_batches_tenant
    ON public.capture_batches (tenant_id, accepted_at DESC);

CREATE TABLE IF NOT EXISTS public.capture_messages (
    capture_message_id  UUID PRIMARY KEY,          -- body_ref 的 uuid 部分
    tenant_id           VARCHAR(255) NOT NULL,
    batch_id            UUID NOT NULL REFERENCES public.capture_batches ON DELETE CASCADE,
    source_conversation_id TEXT NOT NULL,
    source_message_id   TEXT NOT NULL,
    parent_message_id   TEXT,
    sequence_no         BIGINT NOT NULL,
    content_revision    INT NOT NULL,
    role                TEXT NOT NULL,
    body_ref            TEXT NOT NULL,             -- internal://capture/{capture_message_id}
    idempotency_key     TEXT NOT NULL,
    occurred_at         TIMESTAMPTZ NOT NULL,
    captured_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata            JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT uq_capture_messages_idem UNIQUE (tenant_id, idempotency_key),
    CONSTRAINT uq_capture_messages_source_rev
        UNIQUE (tenant_id, source_conversation_id, source_message_id, content_revision),
    CONSTRAINT ck_capture_messages_role CHECK (role IN ('user', 'assistant', 'system', 'tool'))
);
CREATE INDEX IF NOT EXISTS idx_capture_messages_batch
    ON public.capture_messages (tenant_id, batch_id);
CREATE INDEX IF NOT EXISTS idx_capture_messages_conversation
    ON public.capture_messages (tenant_id, source_conversation_id, sequence_no);

CREATE TABLE IF NOT EXISTS public.capture_bodies (
    body_ref           TEXT PRIMARY KEY,           -- internal://capture/{uuid}
    tenant_id          VARCHAR(255) NOT NULL,
    capture_message_id UUID NOT NULL REFERENCES public.capture_messages ON DELETE CASCADE,
    content            TEXT NOT NULL,
    content_type       TEXT NOT NULL DEFAULT 'text/plain',
    sha256             TEXT NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- RLS（对齐 session_dim 等既有惯例）
ALTER TABLE public.capture_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.capture_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.capture_bodies ENABLE ROW LEVEL SECURITY;

CREATE POLICY capture_tenant_isolation ON public.capture_batches
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY capture_tenant_isolation ON public.capture_messages
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY capture_tenant_isolation ON public.capture_bodies
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);

-- Ledger self-registration（710 惯例）
INSERT INTO public.schema_migrations (version, description)
VALUES ('931', 'userscript capture family: capture_batches/capture_messages/capture_bodies with tenant RLS and idempotency uniques (P0-3 M1, ADR userscript-capture-gateway-endpoint D5)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
