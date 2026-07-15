-- 407_request_context_attrs.down.sql
-- 回滚 migration 407。注意：仅回滚 DDL 结构，不处理已写入的数据。

DROP TABLE IF EXISTS public.request_context_attrs;

-- 回滚 customer / project 维度小 DDL（仅当列由本迁移添加时安全）。
-- applications.customer_id / session_dim.project_id 是新增列，可直接 DROP。
ALTER TABLE public.applications DROP COLUMN IF EXISTS customer_id;
ALTER TABLE public.session_dim DROP COLUMN IF EXISTS project_id;
