-- Migration 517: handoff_pending_confirmations
--
-- 日期: 2026-08-16
-- Purpose: 持久化显式 handoff 客户端确认能力，支持 token、幂等、过期和审计关联。
-- Idempotent: YES (IF NOT EXISTS / CREATE INDEX IF NOT EXISTS / FK 先查再加)
-- Down: 517_handoff_pending_confirmations.down.sql
-- Breaking: NO
-- 形态分支: handoff_log_id 的外键按 handoff_logs 的 relkind 探测——
--   534 重建后 handoff_logs 是 RANGE(created_at) 分区父表，PG 要求分区表上的
--   唯一约束包含分区键，REFERENCES handoff_logs(id) 在该形态下必然失败
--   （no unique constraint matching given keys），物理行的 PK 在 handoff_logs_hot(id)；
--   534 之前的存量库 handoff_logs 是带 PK 的 heap 表，本迁移先于 534 执行。
--   （2026-10-11 host-first 从零按通道顺序跑迁移时暴露，通道顺序 534 < 517。）
-- POST_CONDITION: SELECT 1 FROM information_schema.tables WHERE table_name = 'handoff_pending_confirmations'

BEGIN;

CREATE TABLE IF NOT EXISTS handoff_pending_confirmations (
    id UUID PRIMARY KEY,
    tenant_id VARCHAR(64) NOT NULL,
    api_key_id BIGINT NOT NULL,
    previous_session_id VARCHAR(255) NOT NULL,
    token_hash CHAR(64) NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'confirmed', 'expired')),
    expires_at TIMESTAMPTZ NOT NULL,
    confirmed_at TIMESTAMPTZ,
    new_session_id VARCHAR(255),
    idempotency_hash CHAR(64),
    handoff_log_id INTEGER,
    trigger_mode VARCHAR(32) NOT NULL,
    trigger_reason VARCHAR(64) NOT NULL,
    tokens_at_trigger INTEGER NOT NULL,
    context_window INTEGER,
    messages_at_trigger INTEGER NOT NULL,
    tokens_in_session INTEGER NOT NULL,
    summary_engine VARCHAR(32),
    summary_text TEXT,
    handoff_prompt TEXT,
    skill_name VARCHAR(64),
    duration_ms INTEGER,
    proposal_created_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_handoff_pending_active_session
    ON handoff_pending_confirmations (tenant_id, previous_session_id)
    WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_handoff_pending_tenant_id
    ON handoff_pending_confirmations (tenant_id, id);
CREATE INDEX IF NOT EXISTS idx_handoff_pending_expiry
    ON handoff_pending_confirmations (expires_at)
    WHERE status = 'pending';

DO $do$
DECLARE
    handoff_rel_kind "char";
    fk_target text;
BEGIN
    SELECT c.relkind INTO handoff_rel_kind
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public' AND c.relname = 'handoff_logs';

    IF handoff_rel_kind IS NULL THEN
        RAISE EXCEPTION 'public.handoff_logs does not exist; earlier migrations must run first';
    ELSIF handoff_rel_kind = 'p' THEN
        -- 534 之后的分区父表形态：PK 在 handoff_logs_hot 上
        fk_target := 'handoff_logs_hot';
    ELSE
        -- 534 之前的 heap 形态：handoff_logs(id) 自带 PK
        fk_target := 'handoff_logs';
    END IF;

    IF to_regclass('public.' || fk_target) IS NULL THEN
        RAISE EXCEPTION 'FK target public.% does not exist', fk_target;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'public.handoff_pending_confirmations'::regclass
          AND conname = 'fk_handoff_pending_handoff_log'
    ) THEN
        EXECUTE format(
            'ALTER TABLE public.handoff_pending_confirmations
             ADD CONSTRAINT fk_handoff_pending_handoff_log
             FOREIGN KEY (handoff_log_id) REFERENCES public.%I (id) ON DELETE SET NULL',
            fk_target);
    END IF;
END
$do$;

COMMIT;
