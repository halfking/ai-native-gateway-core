-- Migration: 651 供应商质量画像分钟聚合的数据源契约（request_logs_hot）
--
-- Background:
--   internal/quality/minute_aggregator.go（供应商质量画像 Phase-1，引入于
--   6016d0abd）每分钟从 request_logs 聚合 provider_metrics_minute。其原始
--   SQL 引用了 request_logs 上从未存在的列（model_name / created_at /
--   status_code / error_type / endpoint / input_tokens / output_tokens /
--   cost / ttft_ms），导致所有环境的 PostgreSQL 日志每分钟出现一次
--   SQLSTATE 42703 "column ... does not exist"，供应商质量指标从未落过数据。
--
--   2026-09-03 修复把聚合 SQL 重写为真实 schema 列，并把数据源改为
--   request_logs_hot：实时请求写入热表（8 小时滚动窗口），分区表
--   request_logs 由 promote 调度异步承接（见 341_hot_table_independence.sql
--   与 2026-07-13-multimodal-token-fields-hot.sql 的架构说明），分钟级
--   聚合必须读热表才有时效性。
--
--   列映射口径（与 internal/quality/minute_aggregator.go 保持一致）：
--     model_name   → COALESCE(outbound_model, client_model)
--     created_at   → ts
--     status_code  → upstream_status_code（缺失时回退 error_kind 分类，
--                    口径同 domains/providerprofile/adapters.go）
--     ttft_ms      → stream_first_chunk_ms
--     input/output_tokens → prompt_tokens / completion_tokens
--     cost         → cost_usd
--     endpoint     → 常量 'unknown'（写路径未记录端点类型，见 435 表注释）
--   过滤口径：provider_id > 0（providers 表无 id=0，0 为未路由哨兵）；
--   probe_direct_* 探针流量纳入聚合（HTTP 状态码前缀归类，探测侧自身
--   故障如 endpoint_build/internal_error 计 other）。
--
--   本迁移将重写后聚合器依赖的全部列以 ADD COLUMN IF NOT EXISTS 固化为
--   契约：对已具备这些列的环境是 no-op；对从旧快照恢复 / 跨环境同步导致
--   列缺失的环境是自愈，防止 2026-07-13 那类"hot 表列缺失、写路径整体
--   失败"的事故在质量聚合链路上重演。另补一个支撑分钟聚合扫描与供应商
--   时间窗分析的 partial index。
--
-- Idempotent: YES（IF NOT EXISTS，安全重复执行）。

\set ON_ERROR_STOP on

-- 1) 聚合依赖列契约（标准环境均为 no-op；类型与热表现状一致）
ALTER TABLE public.request_logs_hot
    ADD COLUMN IF NOT EXISTS provider_id          BIGINT,
    ADD COLUMN IF NOT EXISTS ts                   TIMESTAMP WITH TIME ZONE,
    ADD COLUMN IF NOT EXISTS outbound_model       TEXT,
    ADD COLUMN IF NOT EXISTS client_model         TEXT,
    ADD COLUMN IF NOT EXISTS success              BOOLEAN,
    ADD COLUMN IF NOT EXISTS error_kind           TEXT,
    ADD COLUMN IF NOT EXISTS upstream_status_code INTEGER,
    ADD COLUMN IF NOT EXISTS latency_ms           INTEGER,
    ADD COLUMN IF NOT EXISTS stream_first_chunk_ms INTEGER,
    ADD COLUMN IF NOT EXISTS prompt_tokens        INTEGER,
    ADD COLUMN IF NOT EXISTS completion_tokens    INTEGER,
    ADD COLUMN IF NOT EXISTS cost_usd             NUMERIC(14,8);

-- 2) 分钟聚合（ts 一分钟窗口 + provider_id IS NOT NULL 过滤 + 按 provider 分组）
--    与供应商时间窗分析的支撑索引。热表为 8 小时滚动窗口，建索引代价有界。
CREATE INDEX IF NOT EXISTS idx_request_logs_hot_provider_ts
    ON public.request_logs_hot (provider_id, ts DESC)
    WHERE provider_id IS NOT NULL;
