-- 950 down：恢复 538 的六值闭集。
-- 注意：若有 in-flight 行 source='credential_recovery'，回退会失败（23514）——
-- 这是有意的 fail-closed：回退方必须先确认没有恢复源任务在途。
ALTER TABLE public.credential_probe_queue
    DROP CONSTRAINT IF EXISTS credential_probe_queue_source_check;
ALTER TABLE public.credential_probe_queue
    ADD CONSTRAINT credential_probe_queue_source_check CHECK (
        source IN ('request_failure', 'periodic', 'external_async', 'admin',
                   'integrity_probe_planner', 'selfcheck')
    );
