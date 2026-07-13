-- 391_route_incidents_audit_safety.sql
-- 2026-07-14: 安全补齐 routing_audit_log + diagnostic_runs + approval_routing_rules
--
-- 设计背景：
--   154 部署 v996 (c20e19b22) 失败 — v996 引用 routing_audit_log.idempotency_key、
--   approval_routing_rules、diagnostic_runs 等对象，但 252 schema_migrations
--   只跑到 389，routing_audit_log 是旧 schema（8 列，无 idempotency_key 等）。
--   390 迁移使用 CREATE TABLE IF NOT EXISTS，不会替换旧表，导致 schema 不匹配。
--
--   本迁移对 390 的设计做幂等增强：
--   - 旧 routing_audit_log：ALTER TABLE ADD COLUMN IF NOT EXISTS 补齐缺失列
--   - diagnostic_runs：CREATE TABLE IF NOT EXISTS
--   - approval_routing_rules：CREATE TABLE IF NOT EXISTS
--   - 索引和 unique constraint 安全添加

BEGIN;

-- ═══════════════════════════════════════════════════════════════
-- 1. routing_audit_log 缺失列补齐
-- ═══════════════════════════════════════════════════════════════
--
-- 旧 schema: id, ts, actor, action, target_type, target_id, before_json, after_json
-- 新 schema 需要: incident_id, tenant_id, confirmation_token_hash, idempotency_key,
--                 request_payload, pre_snapshot, post_snapshot, response_payload,
--                 outcome, failure_reason, diagnostic_run_id, actor_ip_hash, created_at

ALTER TABLE routing_audit_log
    ADD COLUMN IF NOT EXISTS incident_id UUID,
    ADD COLUMN IF NOT EXISTS tenant_id TEXT,
    ADD COLUMN IF NOT EXISTS confirmation_token_hash TEXT,
    ADD COLUMN IF NOT EXISTS idempotency_key TEXT,
    ADD COLUMN IF NOT EXISTS request_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS pre_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS post_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS response_payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN IF NOT EXISTS outcome TEXT,
    ADD COLUMN IF NOT EXISTS failure_reason TEXT,
    ADD COLUMN IF NOT EXISTS diagnostic_run_id UUID,
    ADD COLUMN IF NOT EXISTS actor_ip_hash TEXT,
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Backfill tenant_id for legacy rows
UPDATE routing_audit_log SET tenant_id = 'default' WHERE tenant_id IS NULL;

-- CHECK 约束（已存在则跳过）
-- 2026-07-14 fix: 旧数据有 21 种 action 不在 v996 的 7 种白名单内
-- (tenant.create, authentication.rate_limited 等)。
-- 实际可取 action 集合是 (v996 期望) ∪ (现有 21 种) = 28 种。
-- 写入端通过 AdminMiddleware 二次校验, 这里用宽松的 CHECK 仅保证非空。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE table_name = 'routing_audit_log'
          AND constraint_name LIKE '%action_check%'
    ) THEN
        ALTER TABLE routing_audit_log
            ADD CONSTRAINT routing_audit_log_action_check CHECK (
                action IS NOT NULL AND length(action) > 0
            );
    END IF;
END $$;

-- Idempotency unique index
CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_audit_log_idempotency
    ON routing_audit_log (idempotency_key);

-- 性能索引
CREATE INDEX IF NOT EXISTS idx_routing_audit_log_tenant_ts
    ON routing_audit_log (tenant_id, ts DESC);

CREATE INDEX IF NOT EXISTS idx_routing_audit_log_incident
    ON routing_audit_log (incident_id);

CREATE INDEX IF NOT EXISTS idx_routing_audit_log_action
    ON routing_audit_log (action, ts DESC);

DO $$ BEGIN RAISE NOTICE 'routing_audit_log columns backfilled'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 2. diagnostic_runs 表
-- ═══════════════════════════════════════════════════════════════
CREATE TABLE IF NOT EXISTS diagnostic_runs (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    incident_id         UUID,
    tenant_id           TEXT NOT NULL DEFAULT 'default',
    kind                TEXT NOT NULL
        CHECK (kind IN (
            'direct_upstream_test', 'through_gateway_test',
            'reprobe', 'release_slot', 'reset_slots',
            'reset_availability', 'recover'
        )),
    state               TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed', 'cancelled')),
    started_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at         TIMESTAMPTZ,
    heartbeat_at        TIMESTAMPTZ,
    trigger_source      TEXT,
    error               TEXT,
    summary_json        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_tenant_started
    ON diagnostic_runs (tenant_id, started_at DESC);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_incident
    ON diagnostic_runs (incident_id);

CREATE INDEX IF NOT EXISTS idx_diagnostic_runs_state
    ON diagnostic_runs (state) WHERE state IN ('pending', 'running');

DO $$ BEGIN RAISE NOTICE 'diagnostic_runs table ensured'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 3. approval_routing_rules 表
-- ═══════════════════════════════════════════════════════════════
CREATE TABLE IF NOT EXISTS approval_routing_rules (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL DEFAULT 'default',
    rule_name       TEXT NOT NULL,
    rule_type       TEXT NOT NULL,
    conditions      JSONB NOT NULL DEFAULT '{}'::jsonb,
    approvers       JSONB NOT NULL DEFAULT '[]'::jsonb,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_approval_routing_rules_tenant
    ON approval_routing_rules (tenant_id, enabled);

DO $$ BEGIN RAISE NOTICE 'approval_routing_rules table ensured'; END $$;

-- ═══════════════════════════════════════════════════════════════
-- 4. 验证最终状态
-- ═══════════════════════════════════════════════════════════════
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                   WHERE table_name = 'routing_audit_log'
                     AND column_name = 'idempotency_key') THEN
        RAISE EXCEPTION 'routing_audit_log.idempotency_key was not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'diagnostic_runs') THEN
        RAISE EXCEPTION 'diagnostic_runs was not created';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_tables WHERE tablename = 'approval_routing_rules') THEN
        RAISE EXCEPTION 'approval_routing_rules was not created';
    END IF;
    RAISE NOTICE '391_route_incidents_audit_safety: all objects verified';
END $$;

COMMIT;
