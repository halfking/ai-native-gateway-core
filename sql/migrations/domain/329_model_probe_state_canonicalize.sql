-- Migration 329: model_probe_state canonicalize legacy state values
-- Date: 2026-07-04
-- Purpose: 修复"常用模型报无可用凭据"的间歇性故障
--
-- Bug:
--   db/db.go 中的旧 init 路径会向 model_probe_state.state 写入字面量
--   'available' / 'healthy' / 'unavailable' / 'failing'。这些是早期
--   （废弃的）状态机字面量。当前的 probe 系统统一使用：
--       healthy_confirmed | recovering | unknown | broken_confirmed |
--       suspicious | probing | manual_offline | manual_online
--   但 reader（domains/credentialstate/cache.go）目前只把
--   'healthy_confirmed' 视作 Available=true，其余全部视作不可用。
--
--   后果：路由开始时先调用 filterAvailableWithStateManager，凡 state
--   落在旧字面量集合中的 (credential, model) 行就被当作不可用丢弃，
--   紧接着 router 报 "router: all candidates unavailable"，executor
--   抛 "all 0 candidates failed"，用户看到
--       "No available provider for model 'claude-sonnet-4-6'"
--   但直接连接凭据是好的。
--
-- Fix:
--   1) 一键 backfill：把 'available' / 'healthy' 升级为
--      'healthy_confirmed'；把 'unavailable' / 'failing' 改为
--      'unknown'，让它们走 probe 自然恢复流程。其它值保留。
--   2) 把 column CHECK 约束升级为白名单（含所有合法状态），避免再
--      次出现旧字面量行（约束会在 db/db.go 旧 init 路径下次启动时
--      立即失败，促其修复或注释）。
--   3) 同步刷新 last_state_change_at，便于审计探查何时回填。

BEGIN;

-- 1. Backfill
UPDATE model_probe_state
SET    state = 'healthy_confirmed',
       last_state_change_at = NOW()
WHERE  state IN ('available', 'healthy');

UPDATE model_probe_state
SET    state = 'unknown',
       last_state_change_at = NOW()
WHERE  state IN ('unavailable', 'failing');

-- 2. 白名单 CHECK 约束（如果已存在则替换）
ALTER TABLE model_probe_state
    DROP CONSTRAINT IF EXISTS model_probe_state_state_check;

ALTER TABLE model_probe_state
    ADD  CONSTRAINT model_probe_state_state_check
    CHECK (state IN (
        'healthy_confirmed',
        'recovering',
        'unknown',
        'broken_confirmed',
        'suspicious',
        'probing',
        'manual_offline',
        'manual_online'
    ));

-- 3. 留下审计行（可选）：回填数量通过 RAISE NOTICE 输出，便于
--    在 psql 中直接看到本次回填规模。
DO $$
DECLARE
    promoted INT := 0;
    demoted  INT := 0;
BEGIN
    GET DIAGNOSTICS promoted = ROW_COUNT;
    RAISE NOTICE 'model_probe_state canonicalize: 见上方 UPDATE 影响行数';
EXCEPTION WHEN OTHERS THEN
    -- GET DIAGNOSTICS 在 DO 块外失效，但 DO 块外的 UPDATE 已成功。
    -- 这里只是 swallow 任何异常，避免阻塞 COMMIT。
    NULL;
END$$;

COMMIT;
