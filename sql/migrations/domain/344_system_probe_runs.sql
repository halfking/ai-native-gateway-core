-- ============================================================================
-- Migration 344: system_probe_runs
-- Purpose:  系统监测模块审计表，记录每一次探测执行结果
--           唯一身份 task_id（Redis 自增），用于追溯 SSE 事件
-- Object Type:  TABLE (PARTITIONED, 按天分区, 30d 滚动)
-- Rollback: sql/migrations/domain/344_system_probe_runs.down.sql
-- ============================================================================
--
-- 设计依据: docs/会话优化v2/32-系统监测模块设计.md §6.2
--           rule 33 (分区表), rule 38 (SQL 脚本管理)
--
-- Usage:
--   psql -h "$DB_HOST" -U "$DB_USER" -d <database> -f sql/migrations/domain/344_system_probe_runs.sql
--
-- Verification:
--   SELECT count(*) FROM system_probe_runs WHERE created_at >= now() - interval '1 day';
--   \d system_probe_runs
--
-- Rollback:
--   DROP TABLE IF EXISTS system_probe_runs CASCADE;
-- ===========================================================================

\set ON_ERROR_STOP on

BEGIN;

CREATE TABLE IF NOT EXISTS system_probe_runs (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY,
    task_id               BIGINT NOT NULL,
    task_type             TEXT NOT NULL,
    automaticity          TEXT NOT NULL DEFAULT 'mandatory',
    credential_id         BIGINT NOT NULL,
    provider_id           BIGINT,
    raw_model             TEXT NOT NULL,
    source                TEXT NOT NULL,
    worker_id             TEXT,
    status                TEXT NOT NULL,
    attempt               INT NOT NULL DEFAULT 1,
    max_attempts          INT NOT NULL DEFAULT 3,
    http_status           INT,
    latency_ms            INT,
    dns_ms                INT,
    tls_ms                INT,
    request_url           TEXT,
    request_body_preview  TEXT,
    response_body_preview TEXT,
    err_code              TEXT,
    err_detail            TEXT,
    skip_reason           TEXT,
    recent_request_id     TEXT,
    recent_request_at     TIMESTAMPTZ,
    started_at            TIMESTAMPTZ NOT NULL,
    finished_at           TIMESTAMPTZ NOT NULL,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at),
    CONSTRAINT system_probe_runs_task_type_check CHECK (task_type IN (
        'direct_ping','gateway_ping','chat_minimal','chat_tool','chat_stream','http_ping'
    )),
    CONSTRAINT system_probe_runs_automaticity_check CHECK (automaticity IN (
        'mandatory','automatic'
    )),
    CONSTRAINT system_probe_runs_status_check CHECK (status IN (
        'success','failed','expired','skipped','timeout','network_error'
    )),
    CONSTRAINT system_probe_runs_attempt_check CHECK (attempt >= 1 AND max_attempts >= 1)
) PARTITION BY RANGE (created_at);

CREATE INDEX IF NOT EXISTS idx_system_probe_runs_credential
    ON system_probe_runs (credential_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_provider
    ON system_probe_runs (provider_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_model
    ON system_probe_runs (raw_model, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_status
    ON system_probe_runs (status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_task_id
    ON system_probe_runs (task_id);
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_skip
    ON system_probe_runs (skip_reason)
    WHERE skip_reason IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_system_probe_runs_automaticity
    ON system_probe_runs (automaticity, created_at DESC);

-- 默认分区（吸收任何未匹配的历史/未来日期；Phase 2 由 rule 33 cron 接管）
CREATE TABLE IF NOT EXISTS system_probe_runs_default
    PARTITION OF system_probe_runs DEFAULT;

COMMENT ON TABLE system_probe_runs IS
    '344: 系统监测模块审计表。任务唯一身份 = task_id (Redis INCR)。按天分区，30d 滚动。';
COMMENT ON COLUMN system_probe_runs.task_id IS
    'Redis 自增任务 ID (llmgw:monitor:tasks:counter)，用于跨 Redis/PG 关联。';
COMMENT ON COLUMN system_probe_runs.automaticity IS
    'mandatory = 强制（手工/错误触发）, automatic = 自动（周期/watchdog）。';
COMMENT ON COLUMN system_probe_runs.skip_reason IS
    '最近一次执行被跳过的原因：recent_request_success 等。NULL 表示未被跳过。';
COMMENT ON COLUMN system_probe_runs.dns_ms IS
    'http_ping 专属：DNS 解析耗时（ms）。';
COMMENT ON COLUMN system_probe_runs.tls_ms IS
    'http_ping 专属：TLS 握手耗时（ms）。';

COMMIT;