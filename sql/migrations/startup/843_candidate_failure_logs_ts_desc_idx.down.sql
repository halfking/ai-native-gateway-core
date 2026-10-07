-- 843_candidate_failure_logs_ts_desc_idx.down.sql
-- 回滚 843：只删索引，不动任何数据。
--
-- ⚠️ 删掉后，告警语句里那条**无 WHERE 子句**的
--   `SELECT max(ts) FROM candidate_failure_logs_with_current_month`
--   会退回 §10.106.28 记录的形态：父表臂对 6 万条索引条目做全索引扫描，
--   cost 4566（hot 臂 171 的 26 倍），均 501 ms/次、58,001 次调用。
--   回滚判据：pg_stat_statements 里该告警语句的 total_exec_time
--   在 843 上线后**没有**明显下降。

BEGIN;

DROP INDEX IF EXISTS public.candidate_failure_logs_hot_ts_desc_idx;
DROP INDEX IF EXISTS public.candidate_failure_logs_ts_desc_idx;

COMMIT;
