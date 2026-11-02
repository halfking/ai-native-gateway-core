-- ===========================================================================
-- File:          sql/migrations/startup/929_outbox_events_fresh_install.sql
-- Migration:     929
-- Purpose:       fresh-install 链补齐 outbox_events（P0-3 live E2E 抓到的
--                真实缺口）：该表在 211 是部署后手工补建的（V357，应用侧
--                DDL），从未进入 installer 登记序 ⇒ 全新安装的库没有它，
--                outbox dispatcher 与 userscript capture 的同事务事件写入
--                全部 42P01。幂等：已有环境（211/252）no-op。
-- Down: 929_outbox_events_fresh_install.down.sql（只删本迁移登记，不删表）
-- ===========================================================================

BEGIN;

CREATE TABLE IF NOT EXISTS public.outbox_events (
    id BIGSERIAL PRIMARY KEY,
    event_id TEXT NOT NULL UNIQUE,
    event_type TEXT NOT NULL,
    schema_version INT NOT NULL,
    tenant_id TEXT NOT NULL,
    aggregate_id TEXT NOT NULL DEFAULT '',
    aggregate_version INT NOT NULL DEFAULT 1,
    occurred_at TIMESTAMPTZ NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    last_error TEXT,
    next_retry_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_outbox_events_status_retry
    ON public.outbox_events (status, next_retry_at);

INSERT INTO public.schema_migrations (version, description)
VALUES ('929', 'fresh-install gap: create outbox_events if absent (hand-applied on 211 as V357, never registered in the installer chain; P0-3 live E2E caught it)')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
