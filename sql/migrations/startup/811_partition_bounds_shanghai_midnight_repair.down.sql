-- Migration 811 down: 不提供边界重建的反向操作（恢复 UTC 零点污染边界
-- 只会复发 42P17 overlap 与 2026-11 写入时间炸弹）。本文件仅作占位与提示。
DO $$
BEGIN
    RAISE NOTICE '811 down: no-op by design (UTC-midnight bounds are the defect; see migration header)';
END $$;
