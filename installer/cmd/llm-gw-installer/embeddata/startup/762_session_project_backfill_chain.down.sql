-- 762 down: 拆除项目回填链（触发器/函数/索引）。已回填的
-- session_summaries.gw_project_id 数据保留——它是观测聚合列，且回填后
-- 新流量仍在持续产生，抹掉只会让项目视图再次恒空；如确需清除：
--   UPDATE session_summaries SET gw_project_id = NULL WHERE gw_project_id LIKE 'app:%';
--   DELETE FROM session_project_attribution WHERE evidence->>'chain' = 'startup-762';
--   DELETE FROM project_dim WHERE project_ref LIKE 'app:%' AND synced_from_acc_at IS NULL;

DROP TRIGGER IF EXISTS trg_session_dim_project_attr_ins ON public.session_dim;
DROP TRIGGER IF EXISTS trg_session_dim_project_attr_upd ON public.session_dim;

DROP FUNCTION IF EXISTS public.session_dim_project_attr_trg();
DROP FUNCTION IF EXISTS public.sync_session_project_attr(TEXT, TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS public.gw_resolve_project_ref(TEXT, TEXT);

DROP INDEX IF EXISTS idx_session_summaries_project_null;
