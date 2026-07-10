-- Migration 336 (down): 恢复被删除的 provider_model 和 binding
-- 
-- Warning: 这只是形式上的 down migration，实际无法恢复被删除的数据。
--          如需恢复，请从备份恢复。

BEGIN;

-- 删除添加的 UNIQUE 约束
DROP INDEX IF EXISTS idx_provider_models_unique_std_name;

-- 记录回滚操作
DO $$
BEGIN
    BEGIN
        INSERT INTO runtask_errors (task_name, payload, created_at)
        VALUES (
            'mig_336_down',
            jsonb_build_object(
                'action', 'rollback_dedupe',
                'note', 'UNIQUE index removed, but deleted rows cannot be restored',
                'ran_at', NOW()
            ),
            NOW()
        );
    EXCEPTION WHEN OTHERS THEN
        NULL;
    END;
END $$;

COMMIT;
