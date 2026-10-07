-- 842_credential_model_index_latest_bucket_idx.down.sql
-- 回滚 842：只删索引，不动任何数据。
--
-- ⚠️ 删掉后，refreshIndexSQL 会退回 §10.106.26 记录的形态：
--   对 356,514 行做 HashAggregate 去产出 1,043 组，均 719 ms/次。
--   回滚判据：pg_stat_statements 里 latest_bucket 那条的 total_exec_time
--   在 842 上线后**没有**明显下降。

BEGIN;

DROP INDEX IF EXISTS public.credential_model_index_hot_cred_model_bucket_idx;
DROP INDEX IF EXISTS public.credential_model_index_cred_model_bucket_idx;

COMMIT;