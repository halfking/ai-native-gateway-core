-- 993 down：estimated_cost_cents 回到 bigint（逐分组取整语义随之回到写面——
-- 回滚本迁移必须同时回滚写面 SQL，否则 float 值进 bigint 列靠隐式四舍五入）。
-- lock_timeout=5s：与 up 侧同守卫（ALTER TABLE ACCESS EXCLUSIVE 等锁上限）。
BEGIN;

SET lock_timeout = '5s';

ALTER TABLE public.report_snapshots
    ALTER COLUMN estimated_cost_cents TYPE bigint USING ROUND(estimated_cost_cents);

COMMIT;
