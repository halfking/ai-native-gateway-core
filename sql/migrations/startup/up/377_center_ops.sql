-- 377_center_ops.sql
-- 中心运维模块表

-- 网关实例表
CREATE TABLE IF NOT EXISTS gateway_instances (
    instance_id     TEXT PRIMARY KEY,
    hostname        TEXT NOT NULL,
    ip_address      TEXT NOT NULL,
    region          TEXT,
    version         TEXT NOT NULL,
    build_seq       INT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'online'
        CHECK (status IN ('online', 'offline', 'degraded')),
    started_at      TIMESTAMPTZ NOT NULL,
    last_heartbeat  TIMESTAMPTZ NOT NULL DEFAULT now(),
    registered_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE INDEX IF NOT EXISTS idx_gi_status ON gateway_instances (status);
CREATE INDEX IF NOT EXISTS idx_gi_region ON gateway_instances (region);
CREATE INDEX IF NOT EXISTS idx_gi_heartbeat ON gateway_instances (last_heartbeat DESC);
CREATE INDEX IF NOT EXISTS idx_gi_version ON gateway_instances (version);

-- 实例心跳历史表
CREATE TABLE IF NOT EXISTS instance_heartbeats (
    instance_id     TEXT NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    uptime_secs     BIGINT NOT NULL,
    num_goroutine   INT NOT NULL,
    alloc_mb        DOUBLE PRECISION NOT NULL,
    status          TEXT NOT NULL,
    PRIMARY KEY (instance_id, timestamp)
);

CREATE INDEX IF NOT EXISTS idx_ih_instance ON instance_heartbeats (instance_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_ih_timestamp ON instance_heartbeats (timestamp DESC);

-- 中心命令表
CREATE TABLE IF NOT EXISTS center_commands (
    id              BIGSERIAL PRIMARY KEY,
    command_id      TEXT NOT NULL UNIQUE,
    instance_id     TEXT NOT NULL,
    command         TEXT NOT NULL,
    args            JSONB,
    status          TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'executed', 'failed', 'expired')),
    issued_at       TIMESTAMPTZ NOT NULL,
    issued_by       TEXT NOT NULL,
    expires_at      TIMESTAMPTZ,
    executed_at     TIMESTAMPTZ,
    result          JSONB
);

CREATE INDEX IF NOT EXISTS idx_cc_instance ON center_commands (instance_id, issued_at DESC);
CREATE INDEX IF NOT EXISTS idx_cc_status ON center_commands (status, issued_at DESC);
CREATE INDEX IF NOT EXISTS idx_cc_command_id ON center_commands (command_id);

-- 实例状态报告表
CREATE TABLE IF NOT EXISTS instance_status_reports (
    instance_id     TEXT NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    state           TEXT NOT NULL,
    active_licenses INT NOT NULL DEFAULT 0,
    active_devices  INT NOT NULL DEFAULT 0,
    requests_total  BIGINT NOT NULL DEFAULT 0,
    requests_ok     BIGINT NOT NULL DEFAULT 0,
    requests_err    BIGINT NOT NULL DEFAULT 0,
    avg_latency_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
    p99_latency_ms  DOUBLE PRECISION NOT NULL DEFAULT 0,
    PRIMARY KEY (instance_id, timestamp)
);

CREATE INDEX IF NOT EXISTS idx_isr_instance ON instance_status_reports (instance_id, timestamp DESC);
CREATE INDEX IF NOT EXISTS idx_isr_timestamp ON instance_status_reports (timestamp DESC);
