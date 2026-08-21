-- 560_session_summaries_tenant_uniqueness.down.sql
-- 回滚 560：删除 UNIQUE(tenant_id, session_key) 约束。

ALTER TABLE public.session_summaries
  DROP CONSTRAINT IF EXISTS session_summaries_session_key_per_tenant;