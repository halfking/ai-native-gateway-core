-- Migration 351: delayed-rollback timestamp for probe-driven state changes.
-- Date: 2026-08-13
-- Purpose: support the "mutate now, auto-rollback after T unless confirmed"
-- semantics required by the unified self-check module (需求 6, bullet 3:
-- 自检后对凭据节点的状态的修改、回退的延时处理).
--
-- A successful probe that tentatively restores a node can stamp probe_revert_at
-- = now()+T; the bg/probe_rollback.go ticker reverts the state when the
-- timestamp elapses. A subsequent confirming probe clears probe_revert_at to
-- NULL, so the revert is a one-shot guard against a probe that "passed" but
-- whose effect should not persist without corroboration.
--
-- Mirrors the existing unavailable_recover_at / cooling_until pattern
-- (consumed by bg/credential_recovery.go), so operators already know to read
-- these timestamp columns. Idempotent (ADD COLUMN IF NOT EXISTS).
BEGIN;

ALTER TABLE credential_model_bindings
    ADD COLUMN IF NOT EXISTS probe_revert_at timestamp with time zone;

ALTER TABLE credentials
    ADD COLUMN IF NOT EXISTS probe_revert_at timestamp with time zone;

-- Speed up the rollback worker's due-scan.
CREATE INDEX IF NOT EXISTS idx_credential_model_bindings_probe_revert
    ON credential_model_bindings (probe_revert_at)
    WHERE probe_revert_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_credentials_probe_revert
    ON credentials (probe_revert_at)
    WHERE probe_revert_at IS NOT NULL;

COMMIT;
