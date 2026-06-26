-- ============================================================================
-- llm-gateway-go 路由相关表结构
-- ============================================================================

-- ----------------------------------------------------------------------------
-- Credentials 表 - API凭据（包含敏感信息，不导出数据）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.credentials (
    id SERIAL PRIMARY KEY,
    provider_id INT NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    api_key TEXT NOT NULL,
    api_secret TEXT,
    extra JSONB,
    base_url TEXT,
    concurrency_limit INT NOT NULL DEFAULT 5,
    concurrency_limit_auto INT,
    fp_slot_limit INT NOT NULL DEFAULT 20,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    health_status VARCHAR(32) NOT NULL DEFAULT 'unknown'
        CHECK (health_status IN ('healthy', 'degraded', 'unhealthy', 'unknown')),
    last_health_check_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_credentials_provider ON credentials(provider_id);
CREATE INDEX IF NOT EXISTS idx_credentials_enabled ON credentials(enabled) WHERE enabled = TRUE;
CREATE INDEX IF NOT EXISTS idx_credentials_auto_limit ON credentials(concurrency_limit_auto) WHERE concurrency_limit_auto IS NOT NULL;

-- 凭据槽位限制 CHECK 约束
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'credentials_fp_slot_limit_check'
          AND conrelid = 'credentials'::regclass
    ) THEN
        ALTER TABLE credentials
            ADD CONSTRAINT credentials_fp_slot_limit_check
            CHECK (fp_slot_limit >= 0 AND fp_slot_limit <= 10000);
    END IF;
END $$;

-- ----------------------------------------------------------------------------
-- Credential Model Bindings 表 - 凭据与模型的绑定关系
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.credential_model_bindings (
    id SERIAL PRIMARY KEY,
    credential_id INT NOT NULL REFERENCES credentials(id) ON DELETE CASCADE,
    provider_model_id INT NOT NULL REFERENCES provider_models(id) ON DELETE CASCADE,
    available BOOLEAN NOT NULL DEFAULT TRUE,
    available_reason TEXT,
    unavailable_reason TEXT,
    unavailable_at TIMESTAMPTZ,
    unavailable_recover_at TIMESTAMPTZ,
    weight NUMERIC(5,2) NOT NULL DEFAULT 1.0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT unique_cmb UNIQUE(credential_id, provider_model_id)
);
CREATE INDEX IF NOT EXISTS idx_cmb_credential ON credential_model_bindings(credential_id);
CREATE INDEX IF NOT EXISTS idx_cmb_available ON credential_model_bindings(available) WHERE available = FALSE;
CREATE INDEX IF NOT EXISTS idx_cmb_unavailable_recover_at ON credential_model_bindings(unavailable_recover_at) WHERE available = FALSE;

-- ----------------------------------------------------------------------------
-- Model Probe State 表 - 模型探测状态
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.model_probe_state (
    credential_id INT NOT NULL,
    raw_model_name TEXT NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'idle'
        CHECK (state IN ('idle', 'probing', 'recovering', 'healthy', 'broken_confirmed', 'manual_disabled')),
    consecutive_failures INT NOT NULL DEFAULT 0,
    last_check_at TIMESTAMPTZ,
    next_retry_at TIMESTAMPTZ,
    last_unavailable_reason TEXT,
    last_err_code TEXT,
    next_retry_at_override TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (credential_id, raw_model_name)
);
CREATE INDEX IF NOT EXISTS idx_model_probe_state_retry ON model_probe_state(state, next_retry_at) WHERE state = 'recovering';
CREATE INDEX IF NOT EXISTS idx_mps_credential ON model_probe_state(credential_id);

-- 自动探测回退时间计算函数
CREATE OR REPLACE FUNCTION model_probe_backoff(consecutive_failures INTEGER)
    RETURNS INTERVAL
    LANGUAGE SQL
    IMMUTABLE
AS $$
    SELECT CASE
        WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
        WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
        ELSE                                  INTERVAL '15 minutes'
    END;
$$;

-- ----------------------------------------------------------------------------
-- Passive Probe State 表 - 被动探测状态（Layer 5）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.passive_probe_state (
    credential_id INTEGER NOT NULL,
    raw_model_name TEXT NOT NULL,
    error_kind TEXT NOT NULL,
    consecutive_count INTEGER NOT NULL DEFAULT 0,
    total_recent_count INTEGER NOT NULL DEFAULT 0,
    window_total_count INTEGER NOT NULL DEFAULT 0,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    in_reviewing BOOLEAN NOT NULL DEFAULT FALSE,
    reviewing_until TIMESTAMPTZ,
    final_marked_at TIMESTAMPTZ,
    unavailable_reason TEXT,
    last_response_body_preview TEXT,
    PRIMARY KEY (credential_id, raw_model_name, error_kind)
);
CREATE INDEX IF NOT EXISTS idx_passive_probe_reviewing ON passive_probe_state(in_reviewing, reviewing_until) WHERE in_reviewing = TRUE;

-- ----------------------------------------------------------------------------
-- Routing Overrides 表 - 路由覆盖（手动路由策略）
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.routing_overrides (
    id BIGSERIAL PRIMARY KEY,
    task_type TEXT NOT NULL,
    profile TEXT NOT NULL DEFAULT '',
    mode TEXT NOT NULL CHECK (mode IN ('pin', 'ban')),
    model_chosen TEXT,
    reason TEXT NOT NULL DEFAULT '',
    created_by TEXT,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_routing_overrides_task_profile ON routing_overrides(task_type, profile);
CREATE INDEX IF NOT EXISTS idx_routing_overrides_expires ON routing_overrides(expires_at) WHERE expires_at IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_routing_overrides_unique ON routing_overrides(task_type, profile, COALESCE(model_chosen, ''), mode);

-- ----------------------------------------------------------------------------
-- Routing Overrides Audit 表 - 路由覆盖审计日志
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.routing_overrides_audit (
    id BIGSERIAL PRIMARY KEY,
    ts TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    action TEXT NOT NULL CHECK (action IN ('insert', 'update', 'delete')),
    override_id BIGINT,
    task_type TEXT,
    profile TEXT,
    mode TEXT,
    model_chosen TEXT,
    reason TEXT,
    expires_at TIMESTAMPTZ,
    old_expires_at TIMESTAMPTZ,
    actor TEXT
);
CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_ts ON routing_overrides_audit(ts DESC);
CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_actor_ts ON routing_overrides_audit(actor, ts DESC) WHERE actor IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_routing_overrides_audit_override_ts ON routing_overrides_audit(override_id, ts DESC) WHERE override_id IS NOT NULL;

-- ----------------------------------------------------------------------------
-- Routing Policy 表 - 路由策略
-- ----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS public.routing_policy (
    id SERIAL PRIMARY KEY,
    policy_name TEXT NOT NULL,
    task_type TEXT NOT NULL,
    profile TEXT NOT NULL,
    rules JSONB NOT NULL,
    priority INT NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_routing_policy_task_profile ON routing_policy(task_type, profile);
CREATE INDEX IF NOT EXISTS idx_routing_policy_enabled ON routing_policy(enabled) WHERE enabled = TRUE;

-- ----------------------------------------------------------------------------
-- recent_success_rate 函数 - 计算凭据+模型最近的请求成功率
-- ----------------------------------------------------------------------------
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int);
DROP FUNCTION IF EXISTS recent_success_rate(bigint, text, int, int);
CREATE FUNCTION recent_success_rate(p_credential_id BIGINT,
                                    p_raw_model TEXT,
                                    p_sample_n INT DEFAULT 50,
                                    p_window_hours INT DEFAULT 3)
RETURNS TABLE(rate DOUBLE PRECISION, samples INT)
LANGUAGE sql
STABLE
AS $$
    WITH recent AS (
        SELECT success
        FROM request_logs
        WHERE credential_id = p_credential_id
          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
          AND ts > NOW() - (p_window_hours || ' hours')::interval
        ORDER BY ts DESC
        LIMIT p_sample_n
    )
    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
           COUNT(*)::int
    FROM recent;
$$;