-- ===========================================================================
-- File:          sql/migrations/startup/872_credential_model_index_partitions_heap.down.sql
-- Migration:     872 (down)
-- Database:      llm_gateway
--
-- 回滚语义：本文件**不做回滚**，它只做验证并说明原因（836 down 同纪律）。
--
-- 872 是**再断言**：把 credential_model_index 月度分区推向治理面
-- （columnar_healthcheck 的 should_be_heap 数组 + columnar_insert_only_parents
-- 白名单）早已声明的终态。把它「回滚」= 主动重建一个已知缺陷：
--
--   · 列存 × 5 分钟增量小批量写 = 海量微小 stripe，autoroute 刷新语句
--     （pg_stat_statements in-repo 第一名，mean 2310ms）回退到 2.3s+；
--   · 本族回到 columnar_healthcheck 报 non-compliant 的漂移状态；
--   · columnar 分区从不 autoanalyze，统计信息重新冻结（838 实测）。
--
-- 若确实需要回到 872 之前（复现疑似由 872 引入的故障）：不要跑这个文件。
-- 正确做法是**新建一条迁移**显式写回旧形态（并先把本族加回
-- columnar_insert_only_parents，否则 ensure tick 会立刻再转回 heap），
-- 让「谁在什么时候把缺陷装回去」有台账可查。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '5min';

DO $verify$
DECLARE
    v_n_bad integer;
    v_bad   text;
BEGIN
    -- (a) 白名单仍不含本族
    IF 'credential_model_index' = ANY (columnar_insert_only_parents()) THEN
        RAISE EXCEPTION '872 down (no-op) verification failed: credential_model_index is back in columnar_insert_only_parents(); something after 872 reintroduced it.';
    END IF;

    -- (b) 没有分区留在非 heap 访问方法上
    SELECT count(*), coalesce(string_agg(c.relname, ', '), '(none)')
      INTO v_n_bad, v_bad
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_am   a ON a.oid = c.relam
     WHERE i.inhparent = 'public.credential_model_index'::regclass
       AND a.amname <> 'heap';
    IF v_n_bad > 0 THEN
        RAISE EXCEPTION '872 down (no-op) verification failed: % credential_model_index partition(s) are not heap again: %', v_n_bad, v_bad;
    END IF;

    -- (c) columnar_healthcheck 仍认识这一族
    IF pg_get_functiondef('public.columnar_healthcheck()'::regprocedure) !~ 'credential_model_index' THEN
        RAISE EXCEPTION '872 down (no-op) verification failed: columnar_healthcheck() stopped knowing credential_model_index, so this family''s drift is invisible again.';
    END IF;

    RAISE NOTICE '872 down is a no-op by design: it re-asserted an existing correct state, so there is nothing to undo. Verified instead — whitelist excludes the family, every credential_model_index partition on heap, columnar_healthcheck() aware.';
END
$verify$;

COMMIT;
