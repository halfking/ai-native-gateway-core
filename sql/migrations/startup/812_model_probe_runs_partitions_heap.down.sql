-- Migration 812 down: 不提供 heap→columnar 反向转换（列存分区正是
-- UPDATE 计划被拒的缺陷源；恢复只会复发探针更新器 CTID 错误）。
DO $$
BEGIN
    RAISE NOTICE '812 down: no-op by design (columnar partitions on model_probe_runs are the defect; see migration header)';
END $$;
