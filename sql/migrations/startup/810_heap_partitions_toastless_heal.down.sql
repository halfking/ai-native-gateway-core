-- Migration 810 down: 结构性治愈不可逆（重建出的 heap 分区自带 TOAST，
-- 降级操作只会把库还原成带缺陷状态）。本文件仅作占位与提示。
DO $$
BEGIN
    RAISE NOTICE '810 down: no-op by design (toastless-heap state is the defect; see migration header)';
END $$;
