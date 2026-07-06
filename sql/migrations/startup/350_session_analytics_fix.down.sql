-- 350_session_analytics_fix.down.sql
-- 回滚 350：删除新增的 session_dim、视图、重写的 trigger；
-- 并恢复 310 的原始（坏掉的）trigger 以保持迁移幂等。
--
-- 注意：回滚后聚合链路会重新失效（这是 310 的原始状态）。

BEGIN;

-- 1. 删除兼容视图
DROP VIEW IF EXISTS v_session_analytics;

-- 2. 删除重写的 trigger + 函数
DROP TRIGGER IF EXISTS trg_update_session_summary ON request_logs;
DROP FUNCTION IF EXISTS update_session_summary();

-- 3. 恢复 310 原始（坏掉的）trigger 函数，以便 310.down 可正常清理
CREATE OR REPLACE FUNCTION update_session_summary()
RETURNS TRIGGER AS $$
BEGIN
    -- 占位：310 原始实现引用了不存在的列，这里不恢复其逻辑
    -- 仅保持函数存在，供 310.down 的 DROP TRIGGER/FUNCTION 正常执行
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_update_session_summary
    AFTER INSERT ON request_logs
    FOR EACH ROW
    WHEN (NEW.session_key IS NOT NULL AND NEW.session_key != '')
    EXECUTE FUNCTION update_session_summary();

-- 4. 删除 session_dim 表
DROP TABLE IF EXISTS session_dim CASCADE;

COMMIT;
