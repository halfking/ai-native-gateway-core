-- Migration: 419_node_probe_runs_complete_fields
-- Purpose: 补全 node_probe_runs 表的请求详情字段，用于完整记录probe过程
-- Author: ACC Agent
-- Date: 2026-07-16
-- Refs: 请求链路追踪系统设计方案

-- 新增字段到 node_probe_runs 表
ALTER TABLE node_probe_runs 
  ADD COLUMN IF NOT EXISTS api_model TEXT,                    -- 标准模型名（用户请求的模型名，如 gpt-5.6-luna）
  ADD COLUMN IF NOT EXISTS outbound_model TEXT,               -- 上游模型名（发给provider的模型名）
  ADD COLUMN IF NOT EXISTS provider_id BIGINT,                -- provider ID
  ADD COLUMN IF NOT EXISTS request_url TEXT,                  -- 完整请求URL
  ADD COLUMN IF NOT EXISTS request_headers JSONB,             -- 请求头（已脱敏，不含Authorization）
  ADD COLUMN IF NOT EXISTS request_body TEXT,                 -- 请求body
  ADD COLUMN IF NOT EXISTS response_body TEXT,                -- 响应body（前512字节）
  ADD COLUMN IF NOT EXISTS timeout_at_ms INT,                 -- 超时发生的时间点（毫秒）
  ADD COLUMN IF NOT EXISTS via_proxy BOOLEAN;                 -- 是否通过代理

-- 添加注释
COMMENT ON COLUMN node_probe_runs.api_model IS '用户请求的标准模型名（如 gpt-5.6-luna）';
COMMENT ON COLUMN node_probe_runs.outbound_model IS '发送给provider的outbound模型名';
COMMENT ON COLUMN node_probe_runs.provider_id IS 'provider ID';
COMMENT ON COLUMN node_probe_runs.request_url IS 'probe请求的完整URL';
COMMENT ON COLUMN node_probe_runs.request_headers IS '请求头（已脱敏，不含Authorization/x-api-key）';
COMMENT ON COLUMN node_probe_runs.request_body IS 'probe请求的body';
COMMENT ON COLUMN node_probe_runs.response_body IS '响应body前512字节';
COMMENT ON COLUMN node_probe_runs.timeout_at_ms IS '如果超时，记录超时时长（毫秒）';
COMMENT ON COLUMN node_probe_runs.via_proxy IS 'probeDirect是否通过HTTP代理';

-- 为新增的查询字段创建索引
CREATE INDEX IF NOT EXISTS idx_node_probe_runs_api_model ON node_probe_runs(api_model, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_node_probe_runs_provider ON node_probe_runs(provider_id, started_at DESC);
