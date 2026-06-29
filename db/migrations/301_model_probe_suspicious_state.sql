-- Migration 301: Model probe suspicious state with 2-hour auto-expiry
--
-- Why: 需要实现一个可疑状态系统，其中：
--   1. 模型的可用状态在 2 小时后自动失效，进入 suspicious 状态
--   2. suspicious 状态的模型被调用时，根据结果标记为 available 或 unavailable
--   3. 成功与失败的状态在 2 小时后自动失效，重新进入 suspicious 状态
--   4. 系统后台异步探测 suspicious 状态的模型
--   5. 同一凭据不可超过 2 个并发探测线程
--
-- 状态机设计：
--   available         (成功状态，2小时后→suspicious)
--   unavailable       (失败状态，2小时后→suspicious)
--   suspicious        (可疑状态，等待探测或实际调用验证)
--   probing           (探测中，防止重复探测)
--
-- Spec: 2026-06-28-suspicious-state-auto-expiry

-- ── 1. 扩展 model_probe_state 表，添加 suspicious 状态支持 ──────────────

-- 添加状态过期时间字段
ALTER TABLE model_probe_state
    ADD COLUMN IF NOT EXISTS state_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS marked_suspicious_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS probing_started_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS probing_credential_concurrency INTEGER DEFAULT 0;

-- 为 suspicious 状态索引优化
CREATE INDEX IF NOT EXISTS idx_mps_suspicious_expired
    ON model_probe_state (state_expires_at)
    WHERE state IN ('available', 'unavailable') AND state_expires_at IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_mps_suspicious_pending
    ON model_probe_state (marked_suspicious_at, next_retry_at)
    WHERE state = 'suspicious';

CREATE INDEX IF NOT EXISTS idx_mps_probing
    ON model_probe_state (probing_started_at)
    WHERE state = 'probing';

COMMENT ON COLUMN model_probe_state.state_expires_at IS 
    '状态过期时间：available/unavailable 状态在此时间后自动变为 suspicious';
COMMENT ON COLUMN model_probe_state.marked_suspicious_at IS 
    '标记为 suspicious 状态的时间';
COMMENT ON COLUMN model_probe_state.probing_started_at IS 
    '开始探测的时间，用于检测探测超时';
COMMENT ON COLUMN model_probe_state.probing_credential_concurrency IS 
    '该凭据当前的并发探测数量（冗余字段，用于快速检查）';

-- ── 2. 自动将过期状态标记为 suspicious 的函数 ───────────────────────────

CREATE OR REPLACE FUNCTION model_probe_expire_to_suspicious()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    expired_count INTEGER;
BEGIN
    -- 将所有过期的 available/unavailable 状态标记为 suspicious
    WITH updated AS (
        UPDATE model_probe_state
        SET state = 'suspicious',
            marked_suspicious_at = NOW(),
            state_expires_at = NULL,
            next_retry_at = NOW()  -- 立即安排探测
        WHERE state IN ('available', 'unavailable')
          AND state_expires_at IS NOT NULL
          AND state_expires_at <= NOW()
        RETURNING 1
    )
    SELECT COUNT(*) INTO expired_count FROM updated;
    
    RETURN expired_count;
END;
$$;

COMMENT ON FUNCTION model_probe_expire_to_suspicious() IS 
    '将所有过期的 available/unavailable 状态标记为 suspicious，返回更新的行数';

-- ── 3. 清理超时的 probing 状态 ──────────────────────────────────────────

CREATE OR REPLACE FUNCTION model_probe_cleanup_stuck_probing()
RETURNS INTEGER
LANGUAGE plpgsql
AS $$
DECLARE
    cleaned_count INTEGER;
BEGIN
    -- 将超过 5 分钟仍在 probing 状态的记录重置为 suspicious
    -- 这处理探测进程崩溃或超时的情况
    WITH cleaned AS (
        UPDATE model_probe_state
        SET state = 'suspicious',
            probing_started_at = NULL,
            next_retry_at = NOW() + INTERVAL '2 minutes'
        WHERE state = 'probing'
          AND probing_started_at IS NOT NULL
          AND probing_started_at < NOW() - INTERVAL '5 minutes'
        RETURNING 1
    )
    SELECT COUNT(*) INTO cleaned_count FROM cleaned;
    
    RETURN cleaned_count;
END;
$$;

COMMENT ON FUNCTION model_probe_cleanup_stuck_probing() IS 
    '清理超过 5 分钟仍在 probing 状态的记录，防止探测进程崩溃导致的状态卡死';

-- ── 4. 获取凭据的当前并发探测数 ──────────────────────────────────────────

CREATE OR REPLACE FUNCTION model_probe_credential_concurrency(p_credential_id BIGINT)
RETURNS INTEGER
LANGUAGE SQL
STABLE
AS $$
    SELECT COUNT(*)::INTEGER
    FROM model_probe_state
    WHERE credential_id = p_credential_id
      AND state = 'probing'
      AND probing_started_at > NOW() - INTERVAL '5 minutes';
$$;

COMMENT ON FUNCTION model_probe_credential_concurrency(BIGINT) IS 
    '返回指定凭据当前正在进行的探测数量（5分钟内开始的 probing 状态）';

-- ── 5. 标记模型为可用状态（2小时后过期）─────────────────────────────────

CREATE OR REPLACE FUNCTION model_probe_mark_available(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_latency_ms INTEGER DEFAULT 0
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state, 
         consecutive_successes, consecutive_failures,
         last_attempt_at, next_retry_at, last_status,
         state_expires_at, marked_suspicious_at)
    VALUES 
        (p_credential_id, p_raw_model_name, 'available',
         1, 0,
         NOW(), NOW() + INTERVAL '2 hours', 'ok',
         NOW() + INTERVAL '2 hours', NULL)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'available',
        consecutive_successes = model_probe_state.consecutive_successes + 1,
        consecutive_failures = 0,
        last_attempt_at = NOW(),
        next_retry_at = NOW() + INTERVAL '2 hours',
        last_status = 'ok',
        state_expires_at = NOW() + INTERVAL '2 hours',
        marked_suspicious_at = NULL,
        probing_started_at = NULL;
END;
$$;

COMMENT ON FUNCTION model_probe_mark_available(BIGINT, TEXT, INTEGER) IS 
    '标记模型为可用状态，2小时后自动过期为 suspicious';

-- ── 6. 标记模型为不可用状态（15分钟后过期）───────────────────────────────

CREATE OR REPLACE FUNCTION model_probe_mark_unavailable(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_error_code TEXT,
    p_error_message TEXT DEFAULT ''
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         consecutive_successes, consecutive_failures,
         last_attempt_at, next_retry_at, last_status,
         state_expires_at, marked_suspicious_at,
         last_unavailable_reason, last_err_code)
    VALUES 
         (p_credential_id, p_raw_model_name, 'unavailable',
          0, 1,
          NOW(), NOW() + INTERVAL '15 minutes', 'http_4xx',
          NOW() + INTERVAL '15 minutes', NULL,
          p_error_message, p_error_code)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'unavailable',
        consecutive_successes = 0,
        consecutive_failures = model_probe_state.consecutive_failures + 1,
        last_attempt_at = NOW(),
        next_retry_at = NOW() + INTERVAL '15 minutes',
        last_status = 'http_4xx',
        state_expires_at = NOW() + INTERVAL '15 minutes',
        marked_suspicious_at = NULL,
        probing_started_at = NULL,
        last_unavailable_reason = p_error_message,
        last_err_code = p_error_code;
END;
$$;

COMMENT ON FUNCTION model_probe_mark_unavailable(BIGINT, TEXT, TEXT, TEXT) IS 
    '标记模型为不可用状态，15分钟后自动过期为 suspicious';

-- ── 7. 开始探测（原子性获取探测权限）────────────────────────────────────

CREATE OR REPLACE FUNCTION model_probe_start_probing(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_max_credential_concurrency INTEGER DEFAULT 2
)
RETURNS BOOLEAN
LANGUAGE plpgsql
AS $$
DECLARE
    current_concurrency INTEGER;
    can_probe BOOLEAN := FALSE;
BEGIN
    -- 检查该凭据的当前并发数
    SELECT model_probe_credential_concurrency(p_credential_id) INTO current_concurrency;
    
    -- 如果已达到并发上限，返回 false
    IF current_concurrency >= p_max_credential_concurrency THEN
        RETURN FALSE;
    END IF;
    
    -- 尝试将 suspicious 状态原子性地更新为 probing
    -- 只有当状态确实是 suspicious 时才会更新成功
    WITH updated AS (
        UPDATE model_probe_state
        SET state = 'probing',
            probing_started_at = NOW(),
            last_attempt_at = NOW()
        WHERE credential_id = p_credential_id
          AND raw_model_name = p_raw_model_name
          AND state = 'suspicious'
        RETURNING 1
    )
    SELECT COUNT(*) > 0 INTO can_probe FROM updated;
    
    RETURN can_probe;
END;
$$;

COMMENT ON FUNCTION model_probe_start_probing(BIGINT, TEXT, INTEGER) IS 
    '原子性地尝试获取探测权限。如果凭据并发数未超限且状态为 suspicious，则更新为 probing 并返回 true';

-- ── 8. 视图：待探测的 suspicious 模型 ───────────────────────────────────

CREATE OR REPLACE VIEW v_suspicious_probe_targets AS
SELECT
    mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, '') AS outbound_model_name,
    COALESCE(p.base_url, '') AS base_url,
    COALESCE(p.protocol, 'openai-completions') AS protocol,
    mps.marked_suspicious_at,
    mps.next_retry_at,
    mps.consecutive_failures,
    mps.consecutive_successes,
    model_probe_credential_concurrency(mps.credential_id) AS credential_probe_count
FROM model_probe_state mps
JOIN credentials c ON c.id = mps.credential_id
JOIN providers p ON p.id = c.provider_id
JOIN provider_models pm ON pm.raw_model_name = mps.raw_model_name
    AND EXISTS (
        SELECT 1 FROM credential_model_bindings cmb
        WHERE cmb.credential_id = mps.credential_id
          AND cmb.provider_model_id = pm.id
    )
WHERE mps.state = 'suspicious'
  AND mps.next_retry_at <= NOW()
  AND COALESCE(c.status, 'active') = 'active'
  AND COALESCE(c.lifecycle_status, 'active') = 'active'
  AND COALESCE(c.manual_disabled, FALSE) = FALSE
  AND COALESCE(p.enabled, FALSE) = TRUE
  AND COALESCE(p.manual_disabled, FALSE) = FALSE
  AND model_probe_credential_concurrency(mps.credential_id) < 2
ORDER BY 
    -- 优先探测并发数少的凭据
    model_probe_credential_concurrency(mps.credential_id) ASC,
    -- 然后按等待时间排序
    mps.marked_suspicious_at ASC NULLS LAST,
    mps.next_retry_at ASC
LIMIT 100;

COMMENT ON VIEW v_suspicious_probe_targets IS 
    '待探测的 suspicious 状态模型列表，已过滤凭据并发限制（< 2），按优先级排序';

-- ── 9. 迁移现有数据 ──────────────────────────────────────────────────────

-- 将现有的 healthy_confirmed 状态映射为 available（2小时后过期）
UPDATE model_probe_state
SET state = 'available',
    state_expires_at = NOW() + INTERVAL '2 hours',
    next_retry_at = NOW() + INTERVAL '2 hours'
WHERE state = 'healthy_confirmed';

-- 将现有的 broken_confirmed 状态映射为 unavailable（15分钟后过期）
UPDATE model_probe_state
SET state = 'unavailable',
    state_expires_at = NOW() + INTERVAL '15 minutes',
    next_retry_at = NOW() + INTERVAL '15 minutes'
WHERE state = 'broken_confirmed';

-- 将现有的 recovering/unknown 状态映射为 suspicious（立即可探测）
UPDATE model_probe_state
SET state = 'suspicious',
    marked_suspicious_at = COALESCE(last_attempt_at, NOW()),
    next_retry_at = COALESCE(next_retry_at, NOW())
WHERE state IN ('recovering', 'unknown');

-- 修复：绑定更新必须按 (credential_id + provider_model_id) 精确命中，
-- 不能只靠 raw_model_name + LIMIT 1，否则多 provider 同名模型会串写。
CREATE OR REPLACE FUNCTION model_probe_mark_available(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_latency_ms INTEGER DEFAULT 0
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         consecutive_successes, consecutive_failures,
         last_attempt_at, next_retry_at, last_status,
         state_expires_at, marked_suspicious_at)
    VALUES
        (p_credential_id, p_raw_model_name, 'available',
         1, 0,
         NOW(), NOW() + INTERVAL '2 hours', 'ok',
         NOW() + INTERVAL '2 hours', NULL)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'available',
        consecutive_successes = model_probe_state.consecutive_successes + 1,
        consecutive_failures = 0,
        last_attempt_at = NOW(),
        next_retry_at = NOW() + INTERVAL '2 hours',
        last_status = 'ok',
        state_expires_at = NOW() + INTERVAL '2 hours',
        marked_suspicious_at = NULL,
        probing_started_at = NULL;

    UPDATE credential_model_bindings cmb
    SET available = TRUE,
        unavailable_reason = NULL,
        unavailable_at = NULL,
        unavailable_recover_at = NULL,
        updated_at = NOW()
    FROM provider_models pm
    WHERE cmb.provider_model_id = pm.id
      AND cmb.credential_id = p_credential_id
      AND pm.raw_model_name = p_raw_model_name
      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';
END;
$$;

CREATE OR REPLACE FUNCTION model_probe_mark_unavailable(
    p_credential_id BIGINT,
    p_raw_model_name TEXT,
    p_error_code TEXT,
    p_error_message TEXT DEFAULT ''
)
RETURNS VOID
LANGUAGE plpgsql
AS $$
BEGIN
    INSERT INTO model_probe_state
        (credential_id, raw_model_name, state,
         consecutive_successes, consecutive_failures,
         last_attempt_at, next_retry_at, last_status,
         state_expires_at, marked_suspicious_at,
         last_unavailable_reason, last_err_code)
    VALUES
        (p_credential_id, p_raw_model_name, 'unavailable',
         0, 1,
         NOW(), NOW() + INTERVAL '15 minutes', 'http_4xx',
         NOW() + INTERVAL '15 minutes', NULL,
         p_error_message, p_error_code)
    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
        state = 'unavailable',
        consecutive_successes = 0,
        consecutive_failures = model_probe_state.consecutive_failures + 1,
        last_attempt_at = NOW(),
        next_retry_at = NOW() + INTERVAL '15 minutes',
        last_status = 'http_4xx',
        state_expires_at = NOW() + INTERVAL '15 minutes',
        marked_suspicious_at = NULL,
        probing_started_at = NULL,
        last_unavailable_reason = p_error_message,
        last_err_code = p_error_code;

    UPDATE credential_model_bindings cmb
    SET available = FALSE,
        unavailable_reason = 'probe_' || p_error_code,
        unavailable_at = NOW(),
        unavailable_recover_at = NOW() + INTERVAL '15 minutes',
        updated_at = NOW()
    FROM provider_models pm
    WHERE cmb.provider_model_id = pm.id
      AND cmb.credential_id = p_credential_id
      AND pm.raw_model_name = p_raw_model_name
      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';
END;
$$;
