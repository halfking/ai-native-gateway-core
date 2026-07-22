-- ===========================================================================
-- File:          deploy/sql/migrations/V351__credential_most_used_model.sql
-- Database:      llm_gateway
-- Purpose:       创建 credential_most_used_model 函数，供
--                 bg/credential_selfcheck.go 的 selfcheck worker 使用
-- Status:        active
-- Idempotent:    YES (使用 CREATE OR REPLACE)
--
-- Changelog:
--   2026-07-22  v1.0  Initial creation - 修复 BUG #5：selfcheck worker
--                          调用 credential_most_used_model(int, int)
--                          但 PG 中函数不存在，worker 30+ 小时
--                          silent 失败，每 5min 循环 retry 同一个 cred
-- ===========================================================================
--
-- Background:
--   bg/credential_selfcheck.go:412 调用：
--     SELECT raw_model_name FROM credential_most_used_model($1, 24)
--   该函数在 PG schema 中不存在 (SQLSTATE 42883)。
--   worker 在 cycleOnce() 里 try-catch 吞掉错误，每 5min 选同一个
--   credential = 2 (gpt-5.4)，永远跑不通；自检机制失效 30+ 小时。
--
-- Function design:
--   输入：credential_id (int), lookback_hours (int，默认 24)
--   输出：raw_model_name (text)，失败/无数据返回 NULL
--   逻辑：从 request_logs_hot 按 credential_id 过滤最近 N 小时，
--         按 model 聚合成功调用次数，取 top-1
--
-- Notes:
--   - 使用 request_logs_hot（热表，90 天内）而不是 request_logs 父表
--     因为按 24h 窗口聚合，request_logs_hot 已覆盖
--   - 仅看 success=true 的请求（与 selfcheck worker 语义对齐：
--     "most used by successful traffic"）
--   - 走 partial index idx_request_logs_hot_credential_started
--     （如果存在），保证毫秒级响应
-- ===========================================================================

CREATE OR REPLACE FUNCTION credential_most_used_model(
    p_credential_id INT,
    p_lookback_hours INT DEFAULT 24
) RETURNS TEXT
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_model TEXT;
BEGIN
    -- 主路径：request_logs_hot (热表，毫秒级)
    SELECT rl.model
      INTO v_model
      FROM request_logs_hot rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    IF v_model IS NOT NULL THEN
        RETURN v_model;
    END IF;

    -- Fallback: 冷表 request_logs (90 天后迁过去的)
    SELECT rl.model
      INTO v_model
      FROM request_logs rl
     WHERE rl.credential_id = p_credential_id
       AND rl.success = TRUE
       AND rl.started_at >= now() - make_interval(hours => p_lookback_hours)
       AND rl.model IS NOT NULL
     GROUP BY rl.model
     ORDER BY COUNT(*) DESC, rl.model
     LIMIT 1;

    RETURN v_model;
END;
$$;

COMMENT ON FUNCTION credential_most_used_model(INT, INT) IS
    'Returns the raw_model_name with the most successful requests for a credential in the last N hours. Used by bg/credential_selfcheck.go to pick a fallback probe model.';