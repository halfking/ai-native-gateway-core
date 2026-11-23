-- ===========================================================================
-- File:          sql/migrations/startup/1000_request_metrics.sql
-- Migration:     1000
-- Database:      llm_gateway
-- Purpose:       创建统计专用基础表 request_metrics，支持多维度快速聚合查询
--
-- Status:        active
-- Idempotent:    YES (CREATE TABLE IF NOT EXISTS + 单事务)
-- Dependencies:  无 (独立表)
--
-- Background:
--   当前统计数据源分散 (usage_facts / session_summaries / request_stats_minute)，
--   存在重复计算风险，多表关联逻辑复杂，口径难以统一。
--   
--   本表作为统计专用基础表，特性：
--   1. 每个请求一条记录（唯一性保证）
--   2. 异步填充（不影响请求主路径性能）
--   3. 窄表设计（只保留维度和计量字段）
--   4. 多维度支持（供应商/节点/模型/付款者/使用者/会话/团队等）
--   5. 整型优化（费用以分为单位，避免浮点精度问题）
--   6. 分区表（按时间范围分区，支持高效时间范围查询）
--
-- Affected Rows: 0 (新表创建)
-- Breaking Change: NO (纯新增，不影响既有功能)
-- Rollback Script: 1000_request_metrics.down.sql
--
-- Date: 2026-11-23
-- Author: Kiro AI
-- ===========================================================================

\set ON_ERROR_STOP on
BEGIN;
SET lock_timeout = '5s';
SET statement_timeout = '10min';

-- ============================================================
-- 1. request_metrics 表
-- ============================================================

CREATE TABLE IF NOT EXISTS public.request_metrics (
    -- ============================================================
    -- 主键与时间维度
    -- ============================================================
    request_id         TEXT NOT NULL,           -- 请求唯一标识
    tenant_id          TEXT NOT NULL,           -- 租户ID
    occurred_at        TIMESTAMPTZ NOT NULL,    -- 请求发生时间（分区键）
    occurred_date      DATE NOT NULL,           -- 请求发生日期（冗余，用于按日聚合）
    billing_date       DATE NOT NULL,           -- 记账日期（用于跨日请求的账单归属）
    
    -- ============================================================
    -- 核心维度字段
    -- ============================================================
    provider_id        BIGINT,                  -- 供应商ID（关联 providers 表）
    outbound_model     TEXT,                    -- 外发模型名
    canonical_id       BIGINT,                  -- 标准模型ID（关联 models_canonical）
    
    -- 节点与服务器维度
    gateway_node_id    TEXT,                    -- 处理请求的网关节点ID（服务器标识）
    gateway_instance   TEXT,                    -- 网关实例标识（容器/进程ID）
    endpoint_node      TEXT,                    -- 接入端点节点
    
    -- 付款与使用者维度
    payer_id           TEXT,                    -- 付款者ID（租户/团队/用户）
    payer_type         TEXT,                    -- 付款者类型（tenant/team/user）
    user_id            TEXT,                    -- 实际使用者ID
    
    -- 会话与任务维度
    session_id         TEXT,                    -- 会话ID
    task_id            TEXT,                    -- 任务ID（如果是任务发起）
    
    -- 组织结构维度
    org_id             TEXT,                    -- 组织ID
    team_id            BIGINT,                  -- 团队ID
    
    -- API 维度
    api_key_id         BIGINT,                  -- API 密钥ID
    application_id     BIGINT,                  -- 应用ID
    
    -- ============================================================
    -- 计量字段（整型优化，避免浮点精度问题）
    -- ============================================================
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    cached_tokens      INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens   INTEGER NOT NULL DEFAULT 0,
    total_tokens       INTEGER NOT NULL DEFAULT 0,
    
    -- ============================================================
    -- 费用字段（以分为单位，避免浮点精度问题）
    -- ============================================================
    cost_cents         BIGINT NOT NULL DEFAULT 0,    -- 费用（分，1美元=100分）
    credits_charged    BIGINT NOT NULL DEFAULT 0,    -- 积分消耗
    
    -- ============================================================
    -- 状态与类型
    -- ============================================================
    status             TEXT NOT NULL,                -- success/failed/cancelled
    traffic_class      TEXT NOT NULL DEFAULT 'business',  -- business/probe
    request_type       TEXT,                         -- 业务类型扩展
    error_kind         TEXT,                         -- 错误类型
    
    -- ============================================================
    -- 性能指标
    -- ============================================================
    latency_ms         INTEGER,                      -- 总延迟（毫秒）
    ttft_ms            INTEGER,                      -- 首字节时间（毫秒）
    
    -- ============================================================
    -- 扩展属性（JSONB 灵活存储）
    -- ============================================================
    attributes         JSONB,                        -- 扩展属性
    
    -- ============================================================
    -- 元数据
    -- ============================================================
    source             TEXT NOT NULL DEFAULT 'async_processor',  -- 数据来源
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    
    -- ============================================================
    -- 主键约束
    -- ============================================================
    PRIMARY KEY (request_id, occurred_at)
    
) PARTITION BY RANGE (occurred_at);

-- 创建默认分区（捕获未匹配的记录）
CREATE TABLE IF NOT EXISTS public.request_metrics_default
    PARTITION OF public.request_metrics DEFAULT;

-- ============================================================
-- 2. 索引设计
-- ============================================================

-- 租户+时间（最常用的查询模式：看板/会话统计/对账）
CREATE INDEX IF NOT EXISTS idx_request_metrics_tenant_time
    ON public.request_metrics (tenant_id, occurred_at DESC);

-- 租户+记账日期（按日账单查询）
CREATE INDEX IF NOT EXISTS idx_request_metrics_tenant_billing_date
    ON public.request_metrics (tenant_id, billing_date DESC);

-- 网关节点+时间（节点负载统计）
CREATE INDEX IF NOT EXISTS idx_request_metrics_gateway_node_time
    ON public.request_metrics (gateway_node_id, occurred_at DESC)
    WHERE gateway_node_id IS NOT NULL;

-- 供应商+时间（供应商对账）
CREATE INDEX IF NOT EXISTS idx_request_metrics_provider_time
    ON public.request_metrics (provider_id, occurred_at DESC)
    WHERE provider_id IS NOT NULL;

-- 会话+时间（会话统计）
CREATE INDEX IF NOT EXISTS idx_request_metrics_session_time
    ON public.request_metrics (session_id, occurred_at DESC)
    WHERE session_id IS NOT NULL;

-- 流量类型+时间（探测过滤）
CREATE INDEX IF NOT EXISTS idx_request_metrics_traffic_time
    ON public.request_metrics (traffic_class, occurred_at DESC);

-- 付款者+时间（账单查询）
CREATE INDEX IF NOT EXISTS idx_request_metrics_payer_time
    ON public.request_metrics (payer_id, payer_type, occurred_at DESC)
    WHERE payer_id IS NOT NULL;

-- 团队+时间（团队预算）
CREATE INDEX IF NOT EXISTS idx_request_metrics_team_time
    ON public.request_metrics (team_id, occurred_at DESC)
    WHERE team_id IS NOT NULL;

-- 状态+时间（成功率统计）
CREATE INDEX IF NOT EXISTS idx_request_metrics_status_time
    ON public.request_metrics (status, occurred_at DESC);

-- ============================================================
-- 3. 注释
-- ============================================================

COMMENT ON TABLE public.request_metrics IS '统计专用基础表：每个请求一条记录，支持多维度快速聚合查询。异步填充，不影响请求主路径性能。';

COMMENT ON COLUMN public.request_metrics.request_id IS '请求唯一标识，来自 request_logs.request_id 或 usage_facts.request_id';
COMMENT ON COLUMN public.request_metrics.occurred_at IS '请求发生时间，用于分区和时间范围查询';
COMMENT ON COLUMN public.request_metrics.occurred_date IS '请求发生日期（冗余字段），用于按日聚合查询，避免 DATE(occurred_at) 计算';
COMMENT ON COLUMN public.request_metrics.billing_date IS '记账日期，用于跨日请求的账单归属。通常等于 occurred_date，但跨日请求可能计入次日账单';
COMMENT ON COLUMN public.request_metrics.gateway_node_id IS '处理请求的网关节点ID（服务器标识），用于节点负载统计和故障追踪';
COMMENT ON COLUMN public.request_metrics.gateway_instance IS '网关实例标识（容器ID/进程ID），用于细粒度性能分析';
COMMENT ON COLUMN public.request_metrics.endpoint_node IS '接入端点节点，标识请求从哪个接入点进入';
COMMENT ON COLUMN public.request_metrics.cost_cents IS '费用（分），1美元=100分，避免浮点精度问题。从 usage_facts.cost_amount 转换而来';
COMMENT ON COLUMN public.request_metrics.traffic_class IS 'business=业务流量, probe=探测流量。与 usage_facts.traffic_class 保持一致';
COMMENT ON COLUMN public.request_metrics.payer_type IS 'tenant=租户付费, team=团队付费, user=用户付费';
COMMENT ON COLUMN public.request_metrics.attributes IS '扩展属性，存储未来可能需要的维度字段（JSONB格式）';
COMMENT ON COLUMN public.request_metrics.source IS '数据来源标识（async_processor=异步处理器, manual=手动导入, backfill=补填）';

-- ★ ≥952 自登记纪律（★ 本仓约定：★ 每条迁移自带 schema_migrations 记账行 ⇒
--   否则「应用了却没记账」⇒ 记账链断口（★ 914/928/960/981 同款））。
--   ★ 本行为 2026-11-23 合并轮九补入：★ 上游 `b8f2935f4` 的原件缺这一行、
--   而该迁移**从未被任何库应用过**（★ 三条投递通道都没登记它）⇒ 改字节零分叉风险。
INSERT INTO public.schema_migrations (version, description)
VALUES ('1000', 'request metrics partitioned base table')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
