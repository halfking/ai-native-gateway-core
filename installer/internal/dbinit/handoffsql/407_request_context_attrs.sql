-- 407_request_context_attrs.sql
-- 请求附属特性侧表：承载一条请求的客户端信息、请求信息、会话扩展信息三类附属属性。
--
-- 背景（docs/会话优化v2/07-审计报告）：request_logs 已超载且 request_logs_hot
-- 缺 observability 列（2026-07-13 multimodal P0 事故同型），继续塞列会触发 42703。
-- 故将三类附属属性集中在独立侧表，request_logs INSERT 零改动、零回归风险。
--
-- 与 request_logs 经 request_id 一对一关联；identity_hash 作为跨表主键贯穿
-- client_profiles（画像）。写入 best-effort，失败不阻塞主请求日志。
-- 幂等：ON CONFLICT (request_id) DO UPDATE（支持 upsert 补全）。

CREATE TABLE IF NOT EXISTS public.request_context_attrs (
    request_id       TEXT        NOT NULL,
    ts               TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id        TEXT        NOT NULL DEFAULT 'default',
    gw_session_id    TEXT,
    gw_task_id       TEXT,

    -- ─── 客户端信息 ───
    identity_hash      VARCHAR(64),   -- SHA-256 64hex，跨表主键（↔ client_profiles）
    virtual_client_id  VARCHAR(32),   -- "vc-" + identity_hash[:16]
    virtual_ip         INET,          -- 10.x.x.x 派生 IP（出口伪装用）
    virtual_mac        VARCHAR(17),   -- 02:xx:xx:xx:xx:xx
    agent_name         VARCHAR(255),  -- claude-code/cursor/curl/...
    agent_type         VARCHAR(50),   -- web/cli/api/bot/mobile/unknown
    client_ip          INET,          -- 真实客户端 IP（X-Real-IP > XFF[0] > RemoteAddr）
    client_forwarded_for TEXT,        -- 完整 X-Forwarded-For 链
    api_key_fingerprint VARCHAR(16),  -- SHA-256(rawKey)[:16]
    api_key_id         BIGINT,
    application_id     BIGINT,
    application_code   TEXT,
    owner_user         TEXT,
    end_user_id        TEXT,
    customer_id        BIGINT,        -- application → customer 映射（applications.customer_id）
    client_protocol    VARCHAR(50),   -- openai-chat/anthropic-messages/gemini-generate/openai-responses

    -- ─── 请求信息 ───
    is_retry         BOOLEAN NOT NULL DEFAULT FALSE,
    attempt_no       INT,             -- 网关 failover 轮次
    is_probe         BOOLEAN NOT NULL DEFAULT FALSE,
    origin_stage     VARCHAR(32),     -- self_check/node_probe/system_health/business/probe_*
    turn_no          INT,             -- 会话内轮次（best-effort；权威口径见 ROW_NUMBER 派生查询）
    source_channel   VARCHAR(32),     -- web/api/mcp/agent
    client_request_id TEXT,           -- 客户端重试关联键（X-Gw-Client-Request-Id）

    -- ─── 会话扩展信息 ───
    project_id       TEXT,            -- X-Gw-Project-Id
    session_title    TEXT,            -- 投影自 session_titles/session_summaries
    session_summary  TEXT,
    task_id          TEXT,            -- 冗余 = gw_task_id，便于直查

    -- ─── 取证原材 ───
    fingerprint_raw  JSONB,           -- 原始指纹字段（DeviceSeed/MachineID/Runtime/OS/UA/Profile）

    CONSTRAINT request_context_attrs_pkey PRIMARY KEY (request_id)
);

-- 索引
CREATE INDEX IF NOT EXISTS idx_rca_identity_hash
    ON public.request_context_attrs (identity_hash);
CREATE INDEX IF NOT EXISTS idx_rca_session_turn
    ON public.request_context_attrs (gw_session_id, turn_no);
CREATE INDEX IF NOT EXISTS idx_rca_tenant_ts
    ON public.request_context_attrs (tenant_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_rca_customer
    ON public.request_context_attrs (customer_id);
CREATE INDEX IF NOT EXISTS idx_rca_agent
    ON public.request_context_attrs (agent_name);
CREATE INDEX IF NOT EXISTS idx_rca_probe
    ON public.request_context_attrs (is_probe) WHERE is_probe = TRUE;

-- RLS：tenant 隔离（沿用 session_turn_snapshots 风格）
ALTER TABLE public.request_context_attrs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS request_context_attrs_tenant_isolation ON public.request_context_attrs;
CREATE POLICY request_context_attrs_tenant_isolation ON public.request_context_attrs
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
DROP POLICY IF EXISTS request_context_attrs_super_admin_bypass ON public.request_context_attrs;
CREATE POLICY request_context_attrs_super_admin_bypass ON public.request_context_attrs
    USING (current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true');

COMMENT ON TABLE public.request_context_attrs IS
    '请求附属特性侧表：客户端/请求/会话扩展属性，与 request_logs 经 request_id 关联。写入 best-effort。';

-- ─── customer / project 维度（小 DDL）─────────────────────────────
-- 账户 = application，customer = 所属客户/组织。applications 加 customer_id
-- 让 request_context_attrs.customer_id 有数据源（api_keys JOIN applications）。
ALTER TABLE public.applications ADD COLUMN IF NOT EXISTS customer_id BIGINT;
COMMENT ON COLUMN public.applications.customer_id IS
    '所属客户/组织 ID，用于多租户计费聚合（映射到 request_context_attrs.customer_id）';

-- 会话级项目维度。session_dim 加 project_id 让会话可按项目归类。
ALTER TABLE public.session_dim ADD COLUMN IF NOT EXISTS project_id TEXT;
COMMENT ON COLUMN public.session_dim.project_id IS
    '会话所属项目（X-Gw-Project-Id），用于会话级项目归类';
