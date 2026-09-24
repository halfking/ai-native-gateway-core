-- Migration: 036 — mock_probe_history 历史表（2026-09-24，docs/design/2026-09-23-mock-probe-channel）
--
-- Mock Probe 通道子系统（mock-fast / mock-slow × stream / non-stream = 2x2 探测）
-- 的历史落库表。设计要点：
--   - 独立于 request_logs：mock 探测流量绝不写 request_logs（设计原则
--     "mock 数据不污染真实表"，且避免吃满 request_logs 配额）；
--   - 按天 RANGE 分区 + DEFAULT 分区兜底，与 request_logs 分区策略同构；
--   - 启动兜底：网关在 MockProbeEnabled=true 且配置了 DATABASE_URL 时，
--     每次启动会 best-effort 调用 mock_probe_history_daily_partition()
--     建当日分区（迁移文件名/函数名固定，可重复执行）。
--
-- 幂等性：全部语句 IF NOT EXISTS / OR REPLACE，可在任何库重复执行。
-- 应用方式：与 035 相同 —— 由 ops 在维护窗口手工执行（不走 startup
-- revision-sequence），本文件只负责 schema 定义。

CREATE TABLE IF NOT EXISTS mock_probe_history (
    id              BIGSERIAL,
    probe_time      TIMESTAMPTZ NOT NULL DEFAULT now(),
    channel         TEXT NOT NULL,            -- e.g. 'mock-fast:stream'
    supplier        TEXT NOT NULL,            -- 'mock-fast' | 'mock-slow'
    stream          BOOLEAN NOT NULL,
    protocol        TEXT NOT NULL,            -- 'openai' | 'anthropic' | 'response' | 'gemini'
    latency_ms      INTEGER NOT NULL,
    status_code     INTEGER NOT NULL,
    error_code      TEXT,                     -- nullable，e.g. 'timeout' / 'http_500'
    request_id      TEXT,                     -- 来自上游 mock 响应
    failure_streak  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (id, probe_time)
) PARTITION BY RANGE (probe_time);

-- 默认分区兜底（当日分区函数失败时探测记录仍可落库）
CREATE TABLE IF NOT EXISTS mock_probe_history_default
    PARTITION OF mock_probe_history DEFAULT;

-- 按天分区函数（幂等：当日分区已存在则跳过）
CREATE OR REPLACE FUNCTION mock_probe_history_daily_partition()
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    d DATE := current_date;
    pname TEXT := format('mock_probe_history_%s', to_char(d, 'YYYYMMDD'));
BEGIN
    EXECUTE format(
        'CREATE TABLE IF NOT EXISTS %I PARTITION OF mock_probe_history FOR VALUES FROM (%L) TO (%L)',
        pname, d::timestamptz, (d + INTERVAL '1 day')::timestamptz
    );
END $$;

SELECT mock_probe_history_daily_partition();

-- 查询索引（分区父表普通索引，自动向现有及未来分区传播）
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_supplier_time
    ON mock_probe_history (supplier, probe_time DESC);
CREATE INDEX IF NOT EXISTS idx_mock_probe_history_channel_time
    ON mock_probe_history (channel, probe_time DESC);
