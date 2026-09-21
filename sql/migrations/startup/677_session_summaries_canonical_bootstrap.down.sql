-- 677_session_summaries_canonical_bootstrap.down.sql
-- 回滚 677：仅当表仍处于 bootstrap 创建时的精简 canonical 形态时再 DROP。
--
-- 警告：memora/kxmemory 等同库产品可能依赖 public.session_summaries（哪怕
-- 是 memora 最小结构）。本 down 仅在你明确知道这是 gateway 独占库、且 655
-- 及之后的 reconcile/索引/约束都没跑过时才能执行。否则下游 655/560/572/563
-- 等迁移的引用会一起失效。

DO $$
BEGIN
    IF to_regclass('public.session_summaries') IS NULL THEN
        RAISE NOTICE 'session_summaries bootstrap down: table does not exist, no-op';
        RETURN;
    END IF;
    -- 仅当表上仍只有本次 bootstrap 引入的列（session_key..last_trigger_at +
    -- parent_session_key/handoff_reason 等 01-schema.sql:15000-15051 的列），
    -- 且没有任何 655 之后的扩展列（gw_project_id/search_vector/outcome/...）
    -- 时才允许 drop，避免把后续迁移补出来的列一起带走。
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema='public' AND table_name='session_summaries'
          AND column_name IN ('gw_project_id','search_vector','outcome','agent_type','expert_type')
    ) THEN
        RAISE EXCEPTION 'session_summaries bootstrap down: refuses to drop a table that already carries post-bootstrap columns (gw_project_id/search_vector/outcome/agent_type/expert_type); rollback 655-676 first or run a manual DROP';
    END IF;
    DROP TABLE public.session_summaries;
    RAISE NOTICE 'session_summaries bootstrap down: dropped canonical table';
END $$;