-- Migration 302: Unified Probe Scheduler State Machine
--
-- Why: 统一 ModelProbeRunner 和 SuspiciousProbeRunner，实现智能调度
--   减少重复探测，提高准确性，支持实时反馈和自适应间隔
--
-- 新状态机设计：
--   healthy        (健康状态，定期watchdog验证)
--       ↓ (watchdog发现问题 或 实时请求失败)
--   suspicious     (可疑状态，需要立即验证)
--       ↓ (开始探测)
--   probing        (探测中，防止重复)
--       ↓ (探测失败)
--   failing        (失败状态，快速重试恢复)
--       ↓ (连续3次成功)
--   recovering     (恢复中)
--       ↓ (稳定后)
--   healthy        (回到健康状态)
--
-- 探测优先级：
--   P0 - urgent:     实时请求失败触发 (30秒内探测)
--   P1 - suspicious: 可疑状态 (5分钟内探测)
--   P2 - failing:    失败恢复探测 (按退避策略)
--   P3 - watchdog:   定期健康检查 (自适应间隔: 2-8小时)
--
-- Spec: 2026-06-28-unified-probe-scheduler

-- ── 1. 扩展状态机字段 ──────────────────────────────────────────────────

ALTER TABLE model_probe_state
    ADD COLUMN IF NOT EXISTS probe_priority TEXT DEFAULT 'watchdog',
    ADD COLUMN IF NOT EXISTS last_verified_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS verification_interval INTERVAL DEFAULT '4 hours',
    ADD COLUMN IF NOT EXISTS success_rate_7d NUMERIC(5,2) DEFAULT 0.00,
    ADD COLUMN IF NOT EXISTS consecutive_watchdog_successes INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS last_real_request_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS real_request_success_count INTEGER DEFAULT 0,
    ADD COLUMN IF NOT EXISTS real_request_failure_count INTEGER DEFAULT 0;

-- 添加检查约束
ALTER TABLE model_probe_state
    DROP CONSTRAINT IF EXISTS check_probe_priority;
ALTER TABLE model_probe_state
    ADD CONSTRAINT check_probe_priority 
    CHECK (probe_priority IN ('urgent', 'suspicious', 'failing', 'recovering', 'watchdog'));

-- 添加索引优化查询
CREATE INDEX IF NOT EXISTS idx_mps_priority_next_retry
    ON model_probe_state (probe_priority, next_retry_at)
    WHERE state IN ('suspicious', 'failing', 'recovering');

CREATE INDEX IF NOT EXISTS idx_mps_watchdog_due
    ON model_probe_state (last_verified_at)
    WHERE state = 'healthy' 
      AND last_verified_at + verification_interval <= NOW();

CREATE INDEX IF NOT EXISTS idx_mps_success_rate
    ON model_probe_state (success_rate_7d);

COMMENT ON COLUMN model_probe_state.probe_priority IS 
    '探测优先级: urgent(实时失败), suspicious(可疑), failing(失败恢复), recovering(恢复中), watchdog(定期验证)';
COMMENT ON COLUMN model_probe_state.last_verified_at IS 
    '最后一次验证成功的时间（用于watchdog调度）';
COMMENT ON COLUMN model_probe_state.verification_interval IS 
    '自适应验证间隔（基于历史稳定性）';
COMMENT ON COLUMN model_probe_state.success_rate_7d IS 
    '过去7天的成功率（0-100），用于计算自适应间隔';
COMMENT ON COLUMN model_probe_state.consecutive_watchdog_successes IS 
    '连续watchdog验证成功次数（用于延长验证间隔）';
COMMENT ON COLUMN model_probe_state.last_real_request_at IS 
    '最后一次真实请求的时间';
COMMENT ON COLUMN model_probe_state.real_request_success_count IS 
    '真实请求成功次数（过去24小时）';
COMMENT ON COLUMN model_probe_state.real_request_failure_count IS 
    '真实请求失败次数（过去24小时）';

-- ── 2. 统一状态管理函数 ───────────────────────────────────────────────

-- 2.1 标记为健康状态（探测成功）
CREATE OR REPLACE FUNCTION unified_probe_mark_healthy(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_latency_ms INTEGER DEFAULT 0
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
    new_interval INTERVAL;
BEGIN
    -- 获取当前状态
    SELECT state INTO current_state
    FROM model_probe_state
    WHERE credential_id = p_credential_id 
      AND raw_model_name = p_raw_model_name;

    -- 计算自适应验证间隔（基于连续成功次数）
    SELECT 
        CASE 
            WHEN consecutive_watchdog_successes >= 10 THEN '8 hours'::INTERVAL
            WHEN consecutive_watchdog_successes >= 5 THEN '6 hours'::INTERVAL
            WHEN consecutive_watchdog_successes >= 2 THEN '4 hours'::INTERVAL
            ELSE '2 hours'::INTERVAL
        END INTO new_interval
    FROM model_probe_state
    WHERE credential_id = p_credential_id 
      AND raw_model_name = p_raw_model_name;

    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         consecutive_successes, consecutive_failures,
         last_attempt_at, last_verified_at, next_retry_at,
         probe_priority, verification_interval,
         consecutive_watchdog_successes,
         last_status, probing_started_at)
    VALUES 
        (p_credential_id, p_raw_model_name, 'healthy',
         1, 0,
         NOW(), NOW(), NOW() + COALESCE(new_interval, '4 hours'::INTERVAL),
         'watchdog', COALESCE(new_interval, '4 hours'::INTERVAL),
         1,
         'ok', NULL)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'healthy',
        consecutive_successes = model_probe_state.consecutive_successes + 1,
        consecutive_failures = 0,
        last_attempt_at = NOW(),
        last_verified_at = NOW(),
        next_retry_at = NOW() + COALESCE(new_interval, model_probe_state.verification_interval, '4 hours'::INTERVAL),
        probe_priority = 'watchdog',
        verification_interval = COALESCE(new_interval, model_probe_state.verification_interval),
        consecutive_watchdog_successes = CASE 
            WHEN model_probe_state.probe_priority = 'watchdog' 
            THEN model_probe_state.consecutive_watchdog_successes + 1
            ELSE 1
        END,
        last_status = 'ok',
        probing_started_at = NULL,
        state_expires_at = NULL,
        marked_suspicious_at = NULL;

    -- 同步更新 credential_model_bindings
    UPDATE credential_model_bindings
    SET available = TRUE,
        unavailable_reason = NULL,
        last_probe_at = NOW()
    WHERE credential_id = p_credential_id
      AND provider_model_id = (
          SELECT id FROM provider_models 
          WHERE raw_model_name = p_raw_model_name 
          LIMIT 1
      );
END;
$$;

COMMENT ON FUNCTION unified_probe_mark_healthy(BIGINT, TEXT, INTEGER) IS 
    '标记模型为健康状态，自适应计算下次验证间隔（2-8小时）';

-- 2.2 标记为可疑状态（需要立即验证）
CREATE OR REPLACE FUNCTION unified_probe_mark_suspicious(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_reason TEXT DEFAULT 'watchdog_check'
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         marked_suspicious_at, next_retry_at,
         probe_priority, last_unavailable_reason)
    VALUES 
        (p_credential_id, p_raw_model_name, 'suspicious',
         NOW(), NOW(),
         'suspicious', p_reason)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'suspicious',
        marked_suspicious_at = NOW(),
        next_retry_at = NOW(),
        probe_priority = 'suspicious',
        last_unavailable_reason = p_reason,
        probing_started_at = NULL;
END;
$$;

COMMENT ON FUNCTION unified_probe_mark_suspicious(BIGINT, TEXT, TEXT) IS 
    '标记模型为可疑状态，5分钟内会被探测';

-- 2.3 标记为失败状态（需要恢复探测）
CREATE OR REPLACE FUNCTION unified_probe_mark_failing(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_error_code TEXT,
    p_error_message TEXT DEFAULT '',
    p_retry_after_seconds INTEGER DEFAULT 60
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    current_failures INTEGER;
    backoff_seconds INTEGER;
BEGIN
    -- 获取当前连续失败次数
    SELECT COALESCE(consecutive_failures, 0) INTO current_failures
    FROM model_probe_state
    WHERE credential_id = p_credential_id 
      AND raw_model_name = p_raw_model_name;

    -- 计算退避时间（指数退避，上限60分钟）
    backoff_seconds := LEAST(
        p_retry_after_seconds * POWER(2, LEAST(current_failures, 6)),
        3600
    );

    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         consecutive_successes, consecutive_failures,
         last_attempt_at, next_retry_at,
         probe_priority, last_status,
         last_unavailable_reason, last_err_code,
         probing_started_at, consecutive_watchdog_successes)
    VALUES 
        (p_credential_id, p_raw_model_name, 'failing',
         0, 1,
         NOW(), NOW() + (backoff_seconds || ' seconds')::INTERVAL,
         'failing', 'http_error',
         p_error_message, p_error_code,
         NULL, 0)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'failing',
        consecutive_successes = 0,
        consecutive_failures = model_probe_state.consecutive_failures + 1,
        last_attempt_at = NOW(),
        next_retry_at = NOW() + (backoff_seconds || ' seconds')::INTERVAL,
        probe_priority = 'failing',
        last_status = 'http_error',
        last_unavailable_reason = p_error_message,
        last_err_code = p_error_code,
        probing_started_at = NULL,
        consecutive_watchdog_successes = 0,
        state_expires_at = NULL;

    -- 同步更新 credential_model_bindings
    UPDATE credential_model_bindings
    SET available = FALSE,
        unavailable_reason = 'probe_' || p_error_code,
        last_probe_at = NOW()
    WHERE credential_id = p_credential_id
      AND provider_model_id = (
          SELECT id FROM provider_models 
          WHERE raw_model_name = p_raw_model_name 
          LIMIT 1
      );
END;
$$;

COMMENT ON FUNCTION unified_probe_mark_failing(BIGINT, TEXT, TEXT, TEXT, INTEGER) IS 
    '标记模型为失败状态，使用指数退避策略安排重试';

-- 2.4 实时请求反馈（关键功能）
CREATE OR REPLACE FUNCTION unified_probe_on_real_request(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_success BOOLEAN,
    p_error_message TEXT DEFAULT ''
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
DECLARE
    current_state TEXT;
    current_priority TEXT;
BEGIN
    -- 获取当前状态
    SELECT state, probe_priority INTO current_state, current_priority
    FROM model_probe_state
    WHERE credential_id = p_credential_id 
      AND raw_model_name = p_raw_model_name;

    IF p_success THEN
        -- 成功的请求
        UPDATE model_probe_state
        SET last_real_request_at = NOW(),
            real_request_success_count = real_request_success_count + 1,
            -- 如果当前是可疑或失败状态，立即标记为健康
            state = CASE 
                WHEN state IN ('suspicious', 'failing') THEN 'healthy'
                ELSE state
            END,
            probe_priority = CASE
                WHEN state IN ('suspicious', 'failing') THEN 'watchdog'
                ELSE probe_priority
            END,
            last_verified_at = CASE
                WHEN state IN ('suspicious', 'failing') THEN NOW()
                ELSE last_verified_at
            END,
            next_retry_at = CASE
                WHEN state IN ('suspicious', 'failing') THEN NOW() + verification_interval
                ELSE next_retry_at
            END,
            consecutive_failures = 0,
            probing_started_at = NULL
        WHERE credential_id = p_credential_id 
          AND raw_model_name = p_raw_model_name;

        -- 同步更新 credential_model_bindings
        IF current_state IN ('suspicious', 'failing') THEN
            UPDATE credential_model_bindings
            SET available = TRUE,
                unavailable_reason = NULL
            WHERE credential_id = p_credential_id
              AND provider_model_id = (
                  SELECT id FROM provider_models 
                  WHERE raw_model_name = p_raw_model_name 
                  LIMIT 1
              );
        END IF;
    ELSE
        -- 失败的请求 - 立即标记为 urgent 优先级
        UPDATE model_probe_state
        SET last_real_request_at = NOW(),
            real_request_failure_count = real_request_failure_count + 1,
            state = 'suspicious',
            probe_priority = 'urgent',  -- 最高优先级
            marked_suspicious_at = NOW(),
            next_retry_at = NOW(),  -- 立即安排探测
            last_unavailable_reason = 'real_request_failed: ' || p_error_message,
            probing_started_at = NULL
        WHERE credential_id = p_credential_id 
          AND raw_model_name = p_raw_model_name;

        -- 如果记录不存在，创建一个
        INSERT INTO model_probe_state
            (credential_id, raw_model_name, state,
             probe_priority, marked_suspicious_at, next_retry_at,
             last_real_request_at, real_request_failure_count,
             last_unavailable_reason)
        VALUES
            (p_credential_id, p_raw_model_name, 'suspicious',
             'urgent', NOW(), NOW(),
             NOW(), 1,
             'real_request_failed: ' || p_error_message)
        ON CONFLICT (credential_id, raw_model_name) DO NOTHING;
    END IF;
END;
$$;

COMMENT ON FUNCTION unified_probe_on_real_request(BIGINT, TEXT, BOOLEAN, TEXT) IS 
    '实时请求反馈：成功→可能恢复healthy，失败→立即标记urgent优先级';

-- 2.5 计算7天成功率（后台定期执行）
CREATE OR REPLACE FUNCTION unified_probe_update_success_rate()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    updated_count INTEGER;
BEGIN
    WITH stats AS (
        SELECT 
            credential_id,
            raw_model_name,
            COUNT(*) FILTER (WHERE status = 'ok') * 100.0 / NULLIF(COUNT(*), 0) as rate_7d
        FROM model_probe_runs
        WHERE created_at > NOW() - INTERVAL '7 days'
        GROUP BY credential_id, raw_model_name
    )
    UPDATE model_probe_state mps
    SET success_rate_7d = COALESCE(stats.rate_7d, 0.00)
    FROM stats
    WHERE mps.credential_id = stats.credential_id
      AND mps.raw_model_name = stats.raw_model_name
      AND mps.success_rate_7d <> COALESCE(stats.rate_7d, 0.00);

    GET DIAGNOSTICS updated_count = ROW_COUNT;
    RETURN updated_count;
END;
$$;

COMMENT ON FUNCTION unified_probe_update_success_rate() IS 
    '更新所有模型的7天成功率，用于自适应调度';

-- 2.6 重置24小时计数器（每天执行一次）
CREATE OR REPLACE FUNCTION unified_probe_reset_daily_counters()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    reset_count INTEGER;
BEGIN
    UPDATE model_probe_state
    SET real_request_success_count = 0,
        real_request_failure_count = 0
    WHERE last_real_request_at < NOW() - INTERVAL '24 hours';

    GET DIAGNOSTICS reset_count = ROW_COUNT;
    RETURN reset_count;
END;
$$;

COMMENT ON FUNCTION unified_probe_reset_daily_counters() IS 
    '重置超过24小时未使用模型的请求计数器';

-- ── 3. 统一探测队列视图 ───────────────────────────────────────────────

-- 3.1 Urgent 优先级（实时失败触发）
CREATE OR REPLACE VIEW v_unified_probe_urgent AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.marked_suspicious_at,
    mps.last_unavailable_reason,
    'urgent' as priority,
    1 as priority_order
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
WHERE mps.state = 'suspicious'
  AND mps.probe_priority = 'urgent'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
  )
ORDER BY mps.marked_suspicious_at ASC
LIMIT 20;

COMMENT ON VIEW v_unified_probe_urgent IS 
    'Urgent优先级探测队列（实时失败触发，30秒内探测）';

-- 3.2 Suspicious 优先级
CREATE OR REPLACE VIEW v_unified_probe_suspicious AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.marked_suspicious_at,
    mps.last_unavailable_reason,
    'suspicious' as priority,
    2 as priority_order
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
WHERE mps.state = 'suspicious'
  AND mps.probe_priority = 'suspicious'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
  )
ORDER BY mps.marked_suspicious_at ASC
LIMIT 30;

COMMENT ON VIEW v_unified_probe_suspicious IS 
    'Suspicious优先级探测队列（可疑状态，5分钟内探测）';

-- 3.3 Failing 优先级（失败恢复）
CREATE OR REPLACE VIEW v_unified_probe_failing AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.consecutive_failures,
    mps.last_err_code,
    mps.next_retry_at,
    'failing' as priority,
    3 as priority_order
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
WHERE mps.state = 'failing'
  AND mps.probe_priority = 'failing'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
  )
ORDER BY mps.next_retry_at ASC
LIMIT 20;

COMMENT ON VIEW v_unified_probe_failing IS 
    'Failing优先级探测队列（失败恢复，按退避策略）';

-- 3.4 Watchdog 优先级（定期验证）
CREATE OR REPLACE VIEW v_unified_probe_watchdog AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.last_verified_at,
    mps.verification_interval,
    mps.success_rate_7d,
    'watchdog' as priority,
    4 as priority_order
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
WHERE mps.state = 'healthy'
  AND mps.probe_priority = 'watchdog'
  AND (mps.last_verified_at + mps.verification_interval) <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND EXISTS (
      SELECT 1 FROM credential_model_bindings cmb
      WHERE cmb.credential_id = mps.credential_id
        AND cmb.provider_model_id = pm.id
  )
ORDER BY mps.last_verified_at ASC
LIMIT 50;

COMMENT ON VIEW v_unified_probe_watchdog IS 
    'Watchdog优先级探测队列（定期健康检查，自适应间隔2-8小时）';

-- ── 4. 数据迁移 ────────────────────────────────────────────────────────

-- 4.1 迁移现有状态到新状态机
UPDATE model_probe_state
SET state = CASE
        WHEN state IN ('available', 'healthy_confirmed') THEN 'healthy'
        WHEN state IN ('unavailable', 'broken_confirmed') THEN 'failing'
        WHEN state IN ('suspicious', 'unknown') THEN 'suspicious'
        WHEN state IN ('recovering') THEN 'failing'
        ELSE 'suspicious'
    END,
    probe_priority = CASE
        WHEN state IN ('available', 'healthy_confirmed') THEN 'watchdog'
        WHEN state IN ('unavailable', 'broken_confirmed', 'recovering') THEN 'failing'
        ELSE 'suspicious'
    END,
    last_verified_at = CASE
        WHEN state IN ('available', 'healthy_confirmed') THEN COALESCE(last_attempt_at, NOW())
        ELSE NULL
    END,
    verification_interval = '4 hours'::INTERVAL,
    consecutive_watchdog_successes = CASE
        WHEN state IN ('available', 'healthy_confirmed') THEN GREATEST(consecutive_successes, 1)
        ELSE 0
    END
WHERE state IN ('available', 'unavailable', 'healthy_confirmed', 'broken_confirmed', 'suspicious', 'recovering', 'unknown', 'probing');

-- 4.2 为所有健康模型设置初始验证间隔
UPDATE model_probe_state
SET next_retry_at = COALESCE(last_verified_at, NOW()) + verification_interval
WHERE state = 'healthy' 
  AND next_retry_at IS NULL;
