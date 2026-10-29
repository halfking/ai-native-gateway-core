-- 917 down：撤销拦截审核/放行/硬阻断状态的持久化表。
-- 放行与清单会回落到进程内（重启即失），审核记录不可恢复。
DROP TABLE IF EXISTS sanitize_hardblock_state CASCADE;
DROP TABLE IF EXISTS sanitize_egress_releases CASCADE;
DROP TABLE IF EXISTS sanitize_block_reviews CASCADE;
