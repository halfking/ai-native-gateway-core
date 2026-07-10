-- 375_fault_management.sql
-- 故障自愈模块表
--
-- 支持：
--   1. 故障事件记录与状态跟踪
--   2. 故障规则定义与配置
--   3. 动作执行日志

CREATE TABLE IF NOT EXISTS fault_events (
    id              BIGSERIAL PRIMARY KEY,
    rule_id         BIGINT NOT NULL,
    rule_name       TEXT NOT NULL,
    severity        TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    title           TEXT NOT NULL,
    description     TEXT NOT NULL,
    source          TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'new'
        CHECK (status IN ('new', 'acknowledged', 'resolving', 'resolved', 'ignored')),
    metadata        JSONB,
    detected_at     TIMESTAMPTZ NOT NULL,
    acked_at        TIMESTAMPTZ,
    acked_by        TEXT,
    resolved_at     TIMESTAMPTZ,
    resolved_by     TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_fe_rule ON fault_events (rule_id);
CREATE INDEX IF NOT EXISTS idx_fe_status ON fault_events (status);
CREATE INDEX IF NOT EXISTS idx_fe_severity ON fault_events (severity);
CREATE INDEX IF NOT EXISTS idx_fe_detected ON fault_events (detected_at DESC);

CREATE TABLE IF NOT EXISTS fault_rules (
    id              SERIAL PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    description     TEXT NOT NULL,
    metric          TEXT NOT NULL,
    operator        TEXT NOT NULL CHECK (operator IN ('gte', 'lte', 'eq', 'ne')),
    threshold       DOUBLE PRECISION NOT NULL,
    duration        TEXT NOT NULL,
    severity        TEXT NOT NULL CHECK (severity IN ('info', 'warning', 'error', 'critical')),
    action          TEXT NOT NULL,
    action_config   JSONB,
    enabled         BOOLEAN NOT NULL DEFAULT TRUE,
    cooldown        TEXT NOT NULL DEFAULT '5m',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_fr_enabled ON fault_rules (enabled);
CREATE INDEX IF NOT EXISTS idx_fr_metric ON fault_rules (metric);

CREATE TABLE IF NOT EXISTS fault_action_logs (
    id              BIGSERIAL PRIMARY KEY,
    event_id        BIGINT NOT NULL REFERENCES fault_events(id) ON DELETE CASCADE,
    action          TEXT NOT NULL,
    status          TEXT NOT NULL,
    result          TEXT,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    triggered_at    TIMESTAMPTZ NOT NULL,
    completed_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_fal_event ON fault_action_logs (event_id);
CREATE INDEX IF NOT EXISTS idx_fal_triggered ON fault_action_logs (triggered_at DESC);
