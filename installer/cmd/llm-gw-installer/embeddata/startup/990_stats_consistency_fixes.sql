-- 990_stats_consistency_fixes.sql
-- 统计数据一致性修复（2026-11-19 审计发现）
--
-- 问题修复：
-- 1. session_summaries 添加 total_credits_charged 字段（与 stats_usage_daily 对齐）
-- 2. stats_usage_daily 添加成本分解字段（与 session_summaries 对齐）
-- 3. session_summaries 添加 TTFT 统计字段（与 usage_facts 对齐）
--
-- 参考审计报告: docs/STATS_API_AUDIT.md

BEGIN;
-- ★ 强锁 DDL 预算（与 977/978/979 同口径）：ALTER TABLE 拿不到锁时 5s 快速
--   失败而不是堵住全库（lock_guard 门要求）；10min statement_timeout 兜底。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

-- ═══════════════════════════════════════════════════════════════════════
-- 1. session_summaries 添加积分和 TTFT 字段
-- ═══════════════════════════════════════════════════════════════════════

-- 添加积分字段（与 stats_usage_daily.credits_charged 对齐）
ALTER TABLE session_summaries 
ADD COLUMN IF NOT EXISTS total_credits_charged BIGINT NOT NULL DEFAULT 0;

-- 添加 TTFT (Time To First Token) 统计字段
ALTER TABLE session_summaries 
ADD COLUMN IF NOT EXISTS avg_ttft_ms INT,
ADD COLUMN IF NOT EXISTS min_ttft_ms INT,
ADD COLUMN IF NOT EXISTS max_ttft_ms INT;

-- 添加字段注释
COMMENT ON COLUMN session_summaries.total_credits_charged IS '会话总消费积分（与 stats_usage_daily 口径一致）';
COMMENT ON COLUMN session_summaries.avg_ttft_ms IS '平均首字延迟（毫秒），流式响应关键指标';
COMMENT ON COLUMN session_summaries.min_ttft_ms IS '最小首字延迟（毫秒）';
COMMENT ON COLUMN session_summaries.max_ttft_ms IS '最大首字延迟（毫秒）';

-- ═══════════════════════════════════════════════════════════════════════
-- 2. stats_usage_daily 添加成本分解字段
-- ═══════════════════════════════════════════════════════════════════════

-- 添加成本分解字段（与 session_summaries 对齐）
ALTER TABLE stats_usage_daily 
ADD COLUMN IF NOT EXISTS input_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
ADD COLUMN IF NOT EXISTS output_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0;

-- 添加字段注释
COMMENT ON COLUMN stats_usage_daily.input_cost_usd IS '输入（prompt）成本，USD';
COMMENT ON COLUMN stats_usage_daily.output_cost_usd IS '输出（completion）成本，USD';

-- 验证约束：成本分解之和应等于总成本（允许微小浮点误差）
-- 注意：这个约束只对新数据生效，历史数据需要单独回填
-- ALTER TABLE stats_usage_daily 
-- ADD CONSTRAINT chk_cost_breakdown 
-- CHECK (ABS(cost_usd - (input_cost_usd + output_cost_usd)) < 0.01);

-- ═══════════════════════════════════════════════════════════════════════
-- 3. stats_usage_monthly 添加成本分解字段（与 daily 保持一致）
-- ═══════════════════════════════════════════════════════════════════════

ALTER TABLE stats_usage_monthly 
ADD COLUMN IF NOT EXISTS input_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0,
ADD COLUMN IF NOT EXISTS output_cost_usd NUMERIC(20,8) NOT NULL DEFAULT 0;

COMMENT ON COLUMN stats_usage_monthly.input_cost_usd IS '输入（prompt）成本，USD';
COMMENT ON COLUMN stats_usage_monthly.output_cost_usd IS '输出（completion）成本，USD';

-- ═══════════════════════════════════════════════════════════════════════
-- 4. 更新 session_summaries 触发器函数以支持新字段
-- ═══════════════════════════════════════════════════════════════════════

CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
DECLARE
    v_input_cost DECIMAL(12,6);
    v_output_cost DECIMAL(12,6);
    v_total_cost DECIMAL(12,6);
    v_prompt_tokens BIGINT;
    v_completion_tokens BIGINT;
    v_latency_ms INT;
    v_ttft_ms INT;
    v_credits BIGINT;
    v_status VARCHAR(50);
    v_client_model VARCHAR(100);
    v_upstream_model VARCHAR(100);
    v_work_type VARCHAR(50);
    v_provider VARCHAR(50);
BEGIN
    -- 提取字段（避免多次访问 NEW）
    v_input_cost := COALESCE(NEW.input_cost, 0);
    v_output_cost := COALESCE(NEW.output_cost, 0);
    v_total_cost := COALESCE(NEW.total_cost, 0);
    v_prompt_tokens := COALESCE(NEW.prompt_tokens, 0);
    v_completion_tokens := COALESCE(NEW.completion_tokens, 0);
    v_latency_ms := COALESCE(NEW.latency_ms, 0);
    v_ttft_ms := COALESCE(NEW.ttft_ms, 0);
    v_credits := COALESCE(NEW.credits_charged, 0);
    v_status := NEW.status;
    v_client_model := NEW.client_model;
    v_upstream_model := NEW.upstream_model;
    v_work_type := NEW.work_type;
    v_provider := NEW.provider;

    -- 插入或更新会话摘要
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
        total_credits_charged,
        avg_latency_ms,
        min_latency_ms,
        max_latency_ms,
        avg_ttft_ms,
        min_ttft_ms,
        max_ttft_ms,
        models_used,
        work_types,
        providers,
        client_models,
        updated_at
    ) VALUES (
        NEW.session_key,
        NEW.tenant_id,
        NEW.created_at,
        NEW.created_at,
        1,
        CASE WHEN v_status = 'success' THEN 1 ELSE 0 END,
        CASE WHEN v_status != 'success' THEN 1 ELSE 0 END,
        v_total_cost,
        v_input_cost,
        v_output_cost,
        v_prompt_tokens,
        v_completion_tokens,
        v_credits,
        v_latency_ms,
        v_latency_ms,
        v_latency_ms,
        CASE WHEN v_ttft_ms > 0 THEN v_ttft_ms ELSE NULL END,
        CASE WHEN v_ttft_ms > 0 THEN v_ttft_ms ELSE NULL END,
        CASE WHEN v_ttft_ms > 0 THEN v_ttft_ms ELSE NULL END,
        ARRAY[v_upstream_model]::TEXT[],
        CASE WHEN v_work_type IS NOT NULL THEN ARRAY[v_work_type]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_provider IS NOT NULL THEN ARRAY[v_provider]::TEXT[] ELSE '{}'::TEXT[] END,
        CASE WHEN v_client_model IS NOT NULL THEN ARRAY[v_client_model]::TEXT[] ELSE '{}'::TEXT[] END,
        NOW()
    )
    ON CONFLICT (session_key) DO UPDATE SET
        last_request_at = GREATEST(session_summaries.last_request_at, NEW.created_at),
        request_count = session_summaries.request_count + 1,
        success_count = session_summaries.success_count + CASE WHEN v_status = 'success' THEN 1 ELSE 0 END,
        error_count = session_summaries.error_count + CASE WHEN v_status != 'success' THEN 1 ELSE 0 END,
        total_cost_usd = session_summaries.total_cost_usd + v_total_cost,
        input_cost_usd = session_summaries.input_cost_usd + v_input_cost,
        output_cost_usd = session_summaries.output_cost_usd + v_output_cost,
        total_prompt_tokens = session_summaries.total_prompt_tokens + v_prompt_tokens,
        total_completion_tokens = session_summaries.total_completion_tokens + v_completion_tokens,
        total_credits_charged = session_summaries.total_credits_charged + v_credits,
        avg_latency_ms = (
            (session_summaries.avg_latency_ms * session_summaries.request_count + v_latency_ms) / 
            (session_summaries.request_count + 1)
        )::INT,
        min_latency_ms = LEAST(session_summaries.min_latency_ms, v_latency_ms),
        max_latency_ms = GREATEST(session_summaries.max_latency_ms, v_latency_ms),
        avg_ttft_ms = CASE 
            WHEN v_ttft_ms > 0 THEN (
                COALESCE(session_summaries.avg_ttft_ms * NULLIF((session_summaries.request_count - COALESCE((session_summaries.avg_ttft_ms IS NULL)::int, 0)), 0), 0) + v_ttft_ms
            ) / (session_summaries.request_count - COALESCE((session_summaries.avg_ttft_ms IS NULL)::int, 0) + 1)
            ELSE session_summaries.avg_ttft_ms
        END,
        min_ttft_ms = CASE 
            WHEN v_ttft_ms > 0 THEN LEAST(COALESCE(session_summaries.min_ttft_ms, v_ttft_ms), v_ttft_ms)
            ELSE session_summaries.min_ttft_ms
        END,
        max_ttft_ms = CASE 
            WHEN v_ttft_ms > 0 THEN GREATEST(COALESCE(session_summaries.max_ttft_ms, v_ttft_ms), v_ttft_ms)
            ELSE session_summaries.max_ttft_ms
        END,
        models_used = array_unique_append(session_summaries.models_used, v_upstream_model),
        work_types = array_unique_append(session_summaries.work_types, v_work_type),
        providers = array_unique_append(session_summaries.providers, v_provider),
        client_models = array_unique_append(session_summaries.client_models, v_client_model),
        updated_at = NOW();

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ═══════════════════════════════════════════════════════════════════════
-- 5. 创建数据一致性验证视图
-- ═══════════════════════════════════════════════════════════════════════

CREATE OR REPLACE VIEW stats_consistency_check AS
WITH daily_agg AS (
    SELECT 
        day_utc,
        tenant_id,
        SUM(request_count) as total_requests,
        SUM(total_tokens) as total_tokens,
        SUM(credits_charged) as total_credits,
        SUM(cost_usd) as total_cost,
        SUM(input_cost_usd) as total_input_cost,
        SUM(output_cost_usd) as total_output_cost
    FROM stats_usage_daily
    WHERE dimension_type = 'provider_model'
    GROUP BY day_utc, tenant_id
),
session_agg AS (
    SELECT 
        DATE(first_request_at) as day,
        tenant_id,
        SUM(request_count) as total_requests,
        SUM(total_tokens) as total_tokens,
        SUM(total_credits_charged) as total_credits,
        SUM(total_cost_usd) as total_cost,
        SUM(input_cost_usd) as total_input_cost,
        SUM(output_cost_usd) as total_output_cost
    FROM session_summaries
    GROUP BY day, tenant_id
)
SELECT 
    COALESCE(d.day_utc, s.day) as day,
    COALESCE(d.tenant_id, s.tenant_id) as tenant_id,
    d.total_requests as daily_requests,
    s.total_requests as session_requests,
    d.total_requests - COALESCE(s.total_requests, 0) as request_diff,
    d.total_tokens as daily_tokens,
    s.total_tokens as session_tokens,
    d.total_tokens - COALESCE(s.total_tokens, 0) as token_diff,
    d.total_credits as daily_credits,
    s.total_credits as session_credits,
    d.total_credits - COALESCE(s.total_credits, 0) as credit_diff,
    d.total_cost as daily_cost,
    s.total_cost as session_cost,
    d.total_cost - COALESCE(s.total_cost, 0) as cost_diff,
    d.total_input_cost as daily_input_cost,
    s.total_input_cost as session_input_cost,
    d.total_output_cost as daily_output_cost,
    s.total_output_cost as session_output_cost
FROM daily_agg d
FULL OUTER JOIN session_agg s ON d.day_utc = s.day AND d.tenant_id = s.tenant_id
ORDER BY day DESC, tenant_id;

COMMENT ON VIEW stats_consistency_check IS '统计数据一致性检查视图 - 对比 stats_usage_daily 和 session_summaries 的数据差异';

-- ★ 双账本纪律：≥952 的迁移必须自登记进 schema_migrations（960 立的纪律门）。
INSERT INTO public.schema_migrations (version, description)
VALUES ('990', 'stats_consistency_fixes: 统计数据一致性修复（session_summaries 积分/TTFT 字段 + stats_usage_daily 成本分解 + stats_consistency_check 视图）')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
