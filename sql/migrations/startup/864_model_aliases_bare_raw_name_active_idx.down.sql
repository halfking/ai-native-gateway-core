-- 864_model_aliases_bare_raw_name_active_idx.down.sql
-- 回滚：按名删 864 索引（级联零——普通表单索引），不动数据。
DROP INDEX IF EXISTS public.idx_model_aliases_raw_name_active;
