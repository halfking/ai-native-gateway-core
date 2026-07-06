-- Migration 358: Session Ownership Modeling
-- 2026-07-07: 会话归属（Owner）建模 — 精确到人和会话，支撑租户/用户隔离与用户画像
--
-- 背景：
--   request_logs 已有完整 owner 列（tenant_id/api_key_id/application_id/
--   end_user_id/owner_user/api_key_owner_user/application_code/key_alias），
--   但会话级缺失：session_dim 仅有 tenant_id/task_id，session_summaries 无 owner 列。
--   trigger update_session_summary()（350）UPSERT session_dim 时未传播 owner 字段。
--
-- 本迁移：
--   1. 富化 session_dim：新增主属主列（首个请求的 owner，首值优先）
--   2. 新建 session_owners 多属主明细表（支持同一会话跨用户/密钥）
--   3. 重写 update_session_summary() 传播 owner + 维护 session_owners 累计
--   4. 一次性回填 session_dim / session_owners（幂等，可重复执行）
--   5. 补 session_client_task_matrix 的 unique index（修复 357 CONCURRENTLY bug）
--
-- 用户决策：多维全部保留（owner_user/end_user_id/application_code/api_key_id），
--          支持多属主，租户用户可见自己名下的会话分析。

BEGIN;

-- ============================================================
-- 1. 富化 session_dim（主属主，首个请求的 owner）
-- ============================================================
ALTER TABLE session_dim
    ADD COLUMN IF NOT EXISTS api_key_id         BIGINT,
    ADD COLUMN IF NOT EXISTS application_id     BIGINT,
    ADD COLUMN IF NOT EXISTS application_code   VARCHAR(64),
    ADD COLUMN IF NOT EXISTS owner_user         VARCHAR(128),  -- 主属主 = api_key_owner_user
    ADD COLUMN IF NOT EXISTS api_key_owner_user VARCHAR(128),
    ADD COLUMN IF NOT EXISTS end_user_id        VARCHAR(128),
    ADD COLUMN IF NOT EXISTS client_id          VARCHAR(128);  -- 真正的接入方身份（application_code 优先，回退 api_key_prefix）

CREATE INDEX IF NOT EXISTS idx_session_dim_owner_user
    ON session_dim(tenant_id, owner_user) WHERE owner_user IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_end_user
    ON session_dim(tenant_id, end_user_id) WHERE end_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_client
    ON session_dim(tenant_id, client_id) WHERE client_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_session_dim_apikey
    ON session_dim(tenant_id, api_key_id) WHERE api_key_id IS NOT NULL;

COMMENT ON COLUMN session_dim.owner_user IS '会话主属主（首个请求的 api_key_owner_user），用于会话归属与用户画像';
COMMENT ON COLUMN session_dim.end_user_id IS '会话首个请求的终端用户ID';
COMMENT ON COLUMN session_dim.client_id IS '接入方身份：COALESCE(application_code, api_key_prefix)，替代 client_models[1] 启发式';

-- ============================================================
-- 2. session_owners 多属主明细表
-- ============================================================
CREATE TABLE IF NOT EXISTS session_owners (
    id               BIGSERIAL PRIMARY KEY,
    gw_session_id    VARCHAR(128) NOT NULL,
    tenant_id        VARCHAR(255) NOT NULL,
    owner_user       VARCHAR(128),       -- api_key_owner_user（密钥持有人）
    end_user_id      VARCHAR(128),       -- 终端用户
    api_key_id       BIGINT,
    application_id   BIGINT,
    application_code VARCHAR(64),
    client_id        VARCHAR(128),
    request_count    INT NOT NULL DEFAULT 0,
    total_cost_usd   DECIMAL(14,6) NOT NULL DEFAULT 0,
    success_count    INT NOT NULL DEFAULT 0,
    error_count      INT NOT NULL DEFAULT 0,
    first_seen_at    TIMESTAMPTZ,
    last_seen_at     TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- 一个会话内同一 (owner_user, end_user_id, api_key_id) 组合只一行
    UNIQUE (gw_session_id, tenant_id, owner_user, end_user_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS idx_session_owners_tenant_owner
    ON session_owners(tenant_id, owner_user);
CREATE INDEX IF NOT EXISTS idx_session_owners_tenant_enduser
    ON session_owners(tenant_id, end_user_id);
CREATE INDEX IF NOT EXISTS idx_session_owners_tenant_client
    ON session_owners(tenant_id, client_id);
CREATE INDEX IF NOT EXISTS idx_session_owners_session
    ON session_owners(gw_session_id);

ALTER TABLE session_owners ENABLE ROW LEVEL SECURITY;
CREATE POLICY session_owners_tenant_isolation ON session_owners
    USING (tenant_id = current_setting('app.current_tenant', true)::TEXT);
CREATE POLICY session_owners_super_admin_bypass ON session_owners
    USING (
        current_setting('app.current_role', true) = 'super_admin'
        OR current_setting('app.bypass_rls', true) = 'true'
    );

COMMENT ON TABLE session_owners IS '会话多属主明细：记录同一会话内每个 (owner_user/end_user_id/api_key_id) 组合的累计请求/成本/成功失败，支撑用户画像与按人隔离';

-- ============================================================
-- 3. 重写 update_session_summary() — 传播 owner + 维护 session_owners
--    （CREATE OR REPLACE 覆盖 350 的定义；trigger 绑定保持不变）
-- ============================================================
CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
DECLARE
    v_prompt_tokens   BIGINT;
    v_completion_tokens BIGINT;
    v_total_tokens    BIGINT;
    v_cost            DECIMAL(14,8);
    v_input_cost      DECIMAL(12,6);
    v_output_cost     DECIMAL(12,6);
    v_latency_ms      INT;
    v_is_success      BOOLEAN;
    v_client_model    VARCHAR(100);
    v_upstream_model  VARCHAR(100);
    v_work_type       VARCHAR(50);
    v_provider_code   VARCHAR(100);
    v_gw_session_id   VARCHAR(128);
    v_token_ratio     DECIMAL(10,6);
    v_client_id       VARCHAR(128);
BEGIN
    v_gw_session_id := NEW.gw_session_id;
    IF v_gw_session_id IS NULL OR v_gw_session_id = '' THEN
        RETURN NEW;
    END IF;

    v_prompt_tokens     := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_total_tokens      := v_prompt_tokens + v_completion_tokens;
    v_cost              := COALESCE(NEW.cost_usd, 0);
    v_latency_ms        := COALESCE(NEW.latency_ms, 0);
    v_is_success        := NEW.success;
    v_client_model      := NEW.client_model;
    v_upstream_model    := NEW.outbound_model;
    v_work_type         := NEW.work_type;

    IF v_total_tokens > 0 THEN
        v_token_ratio := v_prompt_tokens::DECIMAL(10,6) / v_total_tokens::DECIMAL(10,6);
    ELSE
        v_token_ratio := 0.5;
    END IF;
    v_input_cost  := (v_cost * v_token_ratio)::DECIMAL(12,6);
    v_output_cost := (v_cost - v_input_cost)::DECIMAL(12,6);

    SELECT p.code INTO v_provider_code
    FROM providers p
    WHERE p.id = NEW.provider_id
    LIMIT 1;

    -- 接入方身份：application_code 优先，回退 api_key_prefix
    v_client_id := COALESCE(NULLIF(TRIM(NEW.application_code), ''), NEW.api_key_prefix);

    -- ---- UPSERT session_dim（维护会话生命周期 + 主属主首值优先） ----
    INSERT INTO session_dim (
        gw_session_id, session_key, tenant_id, task_id, status,
        first_request_at, last_active_at, created_at,
        api_key_id, application_id, application_code,
        owner_user, api_key_owner_user, end_user_id, client_id
    ) VALUES (
        v_gw_session_id, v_gw_session_id, NEW.tenant_id, NEW.gw_task_id, 'active',
        NEW.ts, NEW.ts, NOW(),
        NEW.api_key_id, NEW.application_id, NEW.application_code,
        NEW.api_key_owner_user, NEW.api_key_owner_user, NEW.end_user_id, v_client_id
    )
    ON CONFLICT (gw_session_id) DO UPDATE SET
        last_active_at = GREATEST(session_dim.last_active_at, NEW.ts),
        status = CASE WHEN session_dim.status = 'closed' THEN 'active' ELSE session_dim.status END,
        task_id = COALESCE(session_dim.task_id, NEW.gw_task_id),
        -- 主属主列首值优先（仅在尚未设置时回填）
        api_key_id         = COALESCE(session_dim.api_key_id, NEW.api_key_id),
        application_id     = COALESCE(session_dim.application_id, NEW.application_id),
        application_code   = COALESCE(NULLIF(TRIM(session_dim.application_code), ''), NEW.application_code),
        owner_user         = COALESCE(NULLIF(TRIM(session_dim.owner_user), ''), NEW.api_key_owner_user),
        api_key_owner_user = COALESCE(NULLIF(TRIM(session_dim.api_key_owner_user), ''), NEW.api_key_owner_user),
        end_user_id        = COALESCE(NULLIF(TRIM(session_dim.end_user_id), ''), NEW.end_user_id),
        client_id          = COALESCE(NULLIF(TRIM(session_dim.client_id), ''), v_client_id);

    -- ---- UPSERT session_owners（多属主累计） ----
    INSERT INTO session_owners (
        gw_session_id, tenant_id, owner_user, end_user_id,
        api_key_id, application_id, application_code, client_id,
        request_count, total_cost_usd, success_count, error_count,
        first_seen_at, last_seen_at
    ) VALUES (
        v_gw_session_id, NEW.tenant_id, NEW.api_key_owner_user, NEW.end_user_id,
        NEW.api_key_id, NEW.application_id, NEW.application_code, v_client_id,
        1, v_cost,
        CASE WHEN v_is_success THEN 1 ELSE 0 END,
        CASE WHEN v_is_success THEN 0 ELSE 1 END,
        NEW.ts, NEW.ts
    )
    ON CONFLICT (gw_session_id, tenant_id, owner_user, end_user_id, api_key_id)
    DO UPDATE SET
        request_count  = session_owners.request_count + 1,
        total_cost_usd = session_owners.total_cost_usd + EXCLUDED.total_cost_usd,
        success_count  = session_owners.success_count + EXCLUDED.success_count,
        error_count    = session_owners.error_count + EXCLUDED.error_count,
        last_seen_at   = GREATEST(session_owners.last_seen_at, EXCLUDED.last_seen_at);

    -- ---- UPSERT session_summaries（实时聚合，与 350 一致） ----
    INSERT INTO session_summaries (
        session_key,
        tenant_id,
        first_request_at,
        last_request_at,
        request_count,
        success_count,
        error_count,
        total_cost_usd,
        input_cost_usd,
        output_cost_usd,
        total_prompt_tokens,
        total_completion_tokens,
        avg_latency_ms,
        min_latency_ms,
        max_latency_ms,
        models_used,
        work_types,
        providers,
        client_models,
        updated_at
    ) VALUES (
        v_gw_session_id,
        NEW.tenant_id,
        NEW.ts,
        NEW.ts,
        1,
        CASE WHEN v_is_success THEN 1 ELSE 0 END,
        CASE WHEN v_is_success THEN 0 ELSE 1 END,
        v_cost,
        v_input_cost,
        v_output_cost,
        v_prompt_tokens,
        v_completion_tokens,
        v_latency_ms,
        v_latency_ms,
        v_latency_ms,
        CASE WHEN v_upstream_model IS NOT NULL THEN ARRAY[v_upstream_model]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_work_type IS NOT NULL THEN ARRAY[v_work_type]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_provider_code IS NOT NULL THEN ARRAY[v_provider_code]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_client_model IS NOT NULL THEN ARRAY[v_client_model]::TEXT[] ELSE '{}'::TEXT[] END,
        NOW()
    )
    ON CONFLICT (session_key) DO UPDATE SET
        last_request_at = GREATEST(session_summaries.last_request_at, NEW.ts),
        request_count = session_summaries.request_count + 1,
        success_count = session_summaries.success_count + CASE WHEN v_is_success THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count + CASE WHEN v_is_success THEN 0 ELSE 1 END,
        total_cost_usd = session_summaries.total_cost_usd + v_cost,
        input_cost_usd = session_summaries.input_cost_usd + v_input_cost,
        output_cost_usd = session_summaries.output_cost_usd + v_output_cost,
        total_prompt_tokens = session_summaries.total_prompt_tokens + v_prompt_tokens,
        total_completion_tokens = session_summaries.total_completion_tokens + v_completion_tokens,
        avg_latency_ms = (
            (session_summaries.avg_latency_ms * session_summaries.request_count + v_latency_ms)
            / (session_summaries.request_count + 1)
        )::INT,
        min_latency_ms = LEAST(session_summaries.min_latency_ms, v_latency_ms),
        max_latency_ms = GREATEST(session_summaries.max_latency_ms, v_latency_ms),
        models_used = array_unique_append(session_summaries.models_used, v_upstream_model),
        work_types = array_unique_append(session_summaries.work_types, v_work_type),
        providers = array_unique_append(session_summaries.providers, v_provider_code),
        client_models = array_unique_append(session_summaries.client_models, v_client_model),
        primary_model = (
            SELECT m FROM unnest(session_summaries.models_used) AS m
            ORDER BY m LIMIT 1
        ),
        updated_at = NOW();

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ============================================================
-- 4. 一次性回填（幂等）
-- ============================================================

-- 4.1 回填 session_dim 主属主：取每个 gw_session_id 最早一条 request_log 的 owner 列
--     （仅回填仍为 NULL 的字段，不覆盖已由 trigger 设置的值）
WITH earliest AS (
    SELECT DISTINCT ON (gw_session_id)
        gw_session_id, tenant_id, gw_task_id,
        api_key_id, application_id, application_code,
        owner_user, api_key_owner_user, end_user_id, api_key_prefix, ts
    FROM request_logs
    WHERE gw_session_id IS NOT NULL AND gw_session_id <> ''
    ORDER BY gw_session_id, ts ASC
)
UPDATE session_dim sd
SET
    api_key_id         = COALESCE(sd.api_key_id, e.api_key_id),
    application_id     = COALESCE(sd.application_id, e.application_id),
    application_code   = COALESCE(NULLIF(TRIM(sd.application_code), ''), e.application_code),
    owner_user         = COALESCE(NULLIF(TRIM(sd.owner_user), ''), e.api_key_owner_user),
    api_key_owner_user = COALESCE(NULLIF(TRIM(sd.api_key_owner_user), ''), e.api_key_owner_user),
    end_user_id        = COALESCE(NULLIF(TRIM(sd.end_user_id), ''), e.end_user_id),
    client_id          = COALESCE(NULLIF(TRIM(sd.client_id), ''),
                                  COALESCE(NULLIF(TRIM(e.application_code), ''), e.api_key_prefix)),
    task_id            = COALESCE(NULLIF(TRIM(sd.task_id), ''), e.gw_task_id)
FROM earliest e
WHERE sd.gw_session_id = e.gw_session_id
  AND (sd.api_key_id IS NULL
       OR sd.owner_user IS NULL OR TRIM(sd.owner_user) = ''
       OR sd.client_id IS NULL OR TRIM(sd.client_id) = '');

-- 4.2 回填 session_owners：从 request_logs 聚合每个 (session,owner,end_user,apikey) 组合
--     仅插入尚不存在的组合行（ON CONFLICT DO NOTHING，避免重复回填翻倍）
INSERT INTO session_owners (
    gw_session_id, tenant_id, owner_user, end_user_id,
    api_key_id, application_id, application_code, client_id,
    request_count, total_cost_usd, success_count, error_count,
    first_seen_at, last_seen_at
)
SELECT
    gw_session_id,
    tenant_id,
    api_key_owner_user,
    end_user_id,
    api_key_id,
    application_id,
    application_code,
    COALESCE(NULLIF(TRIM(application_code), ''), api_key_prefix),
    COUNT(*) AS request_count,
    COALESCE(SUM(cost_usd), 0) AS total_cost_usd,
    COUNT(*) FILTER (WHERE success) AS success_count,
    COUNT(*) FILTER (WHERE NOT success) AS error_count,
    MIN(ts) AS first_seen_at,
    MAX(ts) AS last_seen_at
FROM request_logs
WHERE gw_session_id IS NOT NULL AND gw_session_id <> ''
GROUP BY gw_session_id, tenant_id, api_key_owner_user, end_user_id, api_key_id,
         application_id, application_code, api_key_prefix
ON CONFLICT (gw_session_id, tenant_id, owner_user, end_user_id, api_key_id)
DO NOTHING;

-- ============================================================
-- 5. 补 session_client_task_matrix 的 unique index（修复 357 CONCURRENTLY bug）
--    若 357 已先于本迁移执行，此处补建；若 357 在本迁移之后执行，357 自带 IF NOT EXISTS。
-- ============================================================
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_client_task_matrix_uq
    ON session_client_task_matrix(tenant_id, client_id, task_id);

COMMIT;
