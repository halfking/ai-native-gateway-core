-- ===========================================================================
-- File:          sql/migrations/startup/836_supplier_errors_heap_reassert.down.sql
-- Migration:     836 (down)
-- Database:      llm_gateway
--
-- 回滚语义：本文件**不做回滚**，它只做验证并说明原因。
--
-- ── 为什么没有可回滚的东西 ────────────────────────────────────────────
--
-- 836 是**再断言**（re-assert），不是引入新形态的迁移。它的三步全部是把活库
-- 推向一个**已经存在的正确目标**：
--
--   1. ensure_supplier_errors_partition → heap 版（813 的正典体）
--   2. columnar_healthcheck            → 含 supplier_errors 的现行版（基线正典体）
--   3. 现存列存分区 → heap（813 的转换通道）
--
-- 把它「回滚」意味着主动把下面三样东西装回去：
--
--   · ensure 函数重新 `USING columnar` 且 ELSE 分支重新调
--     enforce_columnar_partition ⇒ 每小时 ensure tick 把分区转回列存；
--   · 三个分区重新变列存 ⇒ 任何打到它们的 UPDATE/DELETE 计划报
--     "UPDATE and CTID scans not supported for ColumnarScan"；
--   · columnar_healthcheck 重新不认识这一族 ⇒ 上面两种漂移再次**不可见**。
--
-- 那不是「回到迁移前的状态」，那是**主动制造一个已知缺陷**。所以本文件不删
-- 函数、不转回列存、不摘 healthcheck 的词表项。
--
-- ── 那这个 down 做什么 ────────────────────────────────────────────────
--
-- 验证 836 的三条不变量仍然成立，并在**任何一条不成立时大声失败**。理由：
-- 一个从不执行的 down 会让「回滚之后系统是什么样」这个问题永远没有答案；
-- 而这个 down 给出的答案是可执行的 —— 它每次运行都重新回答一次。
--
-- ⚠ 若确实需要回到 836 之前（例如要复现某个疑似由 836 引入的故障），
--   不要跑这个文件。正确做法是**新建一条迁移**显式写回旧形态，并把理由写
--   在那条迁移里 —— 那样「谁在什么时候把缺陷装回去」有台账可查，而不是
--   一条语义为空的 down。
-- ===========================================================================

BEGIN;

SET LOCAL statement_timeout = '5min';

DO $verify$
DECLARE
    v_def   text;
    v_n_bad integer;
    v_bad   text;
    v_clean text;
BEGIN
    -- (a) ensure 函数仍是 heap 版
    v_def := pg_get_functiondef('public.ensure_supplier_errors_partition(timestamptz)'::regprocedure);
    IF v_def LIKE '%USING columnar%' THEN
        RAISE EXCEPTION '836 down (no-op) verification failed: ensure_supplier_errors_partition has gone back to USING columnar. Something after 836 reintroduced it; find that migration instead of relying on this down.';
    END IF;
    v_clean := regexp_replace(v_def, '--.*$', '', 'g');
    IF v_clean ~ 'enforce_columnar_partition' THEN
        RAISE EXCEPTION '836 down (no-op) verification failed: ensure_supplier_errors_partition calls enforce_columnar_partition in executable code again.';
    END IF;

    -- (b) 没有分区留在非 heap 访问方法上
    SELECT count(*), coalesce(string_agg(c.relname, ', '), '(none)')
      INTO v_n_bad, v_bad
      FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_am   a ON a.oid = c.relam
     WHERE i.inhparent = 'public.supplier_errors'::regclass
       AND a.amname <> 'heap';
    IF v_n_bad > 0 THEN
        RAISE EXCEPTION '836 down (no-op) verification failed: % supplier_errors partition(s) are not heap again: %', v_n_bad, v_bad;
    END IF;

    -- (c) columnar_healthcheck 仍认识这一族
    IF pg_get_functiondef('public.columnar_healthcheck()'::regprocedure) !~ 'supplier_errors' THEN
        RAISE EXCEPTION '836 down (no-op) verification failed: columnar_healthcheck() stopped knowing supplier_errors, so this family''s drift is invisible again.';
    END IF;

    RAISE NOTICE '836 down is a no-op by design: it re-asserted an existing correct state, so there is nothing to undo. Verified instead — heap-only ensure function, every supplier_errors partition on heap, columnar_healthcheck() aware of this family.';
END
$verify$;

COMMIT;
