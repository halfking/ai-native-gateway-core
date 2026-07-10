-- Migration 337: routing_health_checks 健康检查发现问题表
-- Date: 2026-07-10
-- Purpose: 路由健康检查结果持久化，支持人工确认/自动修复/忽略
--
-- 背景: claude-sonnet-5/fable-5 路由失败事件暴露了多个层叠 bug：
--   1. provider_models.canonical_id NULL
--   2. credential_model_bindings.billing_mode 不匹配 plan_type
--   3. model_probe_state 缺失
-- 本表让运维持续发现类似问题，无需等代码上线。

BEGIN;

CREATE TABLE IF NOT EXISTS routing_health_checks (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    check_id        text NOT NULL,                      -- 'canonical_id_null' / 'billing_mismatch' / 'probe_missing' / ...
    severity        text NOT NULL DEFAULT 'warning',    -- 'critical' / 'warning' / 'info'
    entity_type     text NOT NULL,                      -- 'provider_models' / 'credential_model_bindings' / 'models_canonical' / ...
    entity_id       bigint,                             -- 对应表的主键
    entity_name     text NOT NULL DEFAULT '',            -- raw_model_name / canonical_name / credential_id:模型 等可读标识
    detail          text NOT NULL DEFAULT '',            -- 诊断详情
    fix_sql         text NOT NULL DEFAULT '',            -- 建议的修复 SQL（可直接在 DB 执行）
    status          text NOT NULL DEFAULT 'open',        -- 'open' / 'auto_fixed' / 'manual_fixed' / 'dismissed'
    auto_fixed_at   timestamptz,
    auto_fix_result text,                               -- 'applied' / 'failed: ...'
    dismissed_at    timestamptz,
    dismissed_by    text,
    dismissed_reason text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT routing_health_checks_status_check CHECK (status IN ('open','auto_fixed','manual_fixed','dismissed')),
    CONSTRAINT routing_health_checks_severity_check CHECK (severity IN ('critical','warning','info')),
    CONSTRAINT routing_health_checks_check_id_unique_per_entity UNIQUE (check_id, entity_type, entity_id)
);

CREATE INDEX IF NOT EXISTS idx_rhc_status ON routing_health_checks(status) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS idx_rhc_check_id ON routing_health_checks(check_id);
CREATE INDEX IF NOT EXISTS idx_rhc_severity ON routing_health_checks(severity, created_at DESC);

COMMENT ON TABLE  routing_health_checks IS '路由健康检查发现问题（自动检查 → 预警 → 修复/忽略）';
COMMENT ON COLUMN routing_health_checks.fix_sql     IS '建议修复 SQL，可直接复制到 psql 执行';
COMMENT ON COLUMN routing_health_checks.check_id    IS '检查类型：canonical_id_null / billing_mismatch / probe_missing / family_unknown / alias_missing';

COMMIT;
