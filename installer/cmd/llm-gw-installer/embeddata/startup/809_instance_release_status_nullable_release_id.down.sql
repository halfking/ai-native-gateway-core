-- 809 down: 恢复 instance_release_status.release_id 的 NOT NULL。
--
-- ⚠ 若表内已存在 release_id IS NULL 的行（即「上报的版本还没有对应
--   releases 行」这种状态已经被写入），本回滚会失败并中止：
--     ERROR: column "release_id" contains null values
--     SQLSTATE 23502
--   这是一次**有意的显式拒绝**，不是缺陷：恢复 NOT NULL 就等于重新
--   宣判「没有 release 的上报不该被存下来」，而那正是 809 修掉的
--   那个 23503 丢报 bug。审计/迁移脚本应先决定这些行如何处置
--   （补建 releases 行，或按上报时间窗删除），再执行本文件。
--
BEGIN;

DROP INDEX IF EXISTS public.idx_irs_release;

ALTER TABLE public.instance_release_status
  ALTER COLUMN release_id SET NOT NULL;

COMMIT;
