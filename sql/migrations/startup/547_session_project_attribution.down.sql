-- 547_session_project_attribution.down.sql
-- 两张表均为 547 新建，回滚时一并删除。
--
-- 用默认的 RESTRICT 而不是 CASCADE：将来若有视图/外键依赖 project_dim，
-- CASCADE 会静默把它们一起删掉，RESTRICT 则会明确报错让人先处理依赖。
--
-- session_dim.project_id（migration 407）和 request_logs 本身不属于 547，
-- 必须在回滚后保持原样；这里只删除 547 自己新增的索引。

DROP INDEX IF EXISTS public.idx_request_logs_tenant_session_ts;

DROP TABLE IF EXISTS public.session_project_attribution;
DROP TABLE IF EXISTS public.project_dim;
