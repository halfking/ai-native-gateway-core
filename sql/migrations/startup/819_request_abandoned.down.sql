-- ===========================================================================
-- File:          sql/migrations/startup/819_request_abandoned.down.sql
-- Migration:     819 down
-- Database:      llm_gateway
-- Purpose:       删除 public.request_abandoned。
--
-- ⚠️ **回滚 819 等于自愿放弃「请求开始了却没结束」这个事实的落点。**
--   回滚后本表消失，而写方（telemetry client 的
--   markRequestAbandonedPending / clearRequestAbandonedPending）**仍然会
--   照常调用**——它们是 best-effort fail-open 的，表不存在只会变成
--   每天几条 slog.Warn，而请求日志主写不受影响。
--   ⇒ 于是系统回到 §9.66 描述的「零落点」状态，且**表面上一切正常**。
--
-- 所以 down 只在两种情况下是正确答案：
--   ① 纯粹为了在装 819 之前把库退回 818 的状态（正常回滚路径）；
--   ② 需要复现「零落点」的行为来对照排查。
-- **绝不要**因为「819 让写入变慢了/表在涨」就回滚——先查是不是别的原因
--   （本表稳态是每天个位数行量；真涨到异常量说明终态 DELETE 那一路坏了，
--   而那正是 819 要抓的东西，回滚等于把它藏起来）。
--
-- 表里的数据在 down 时会被一并删除且**不可恢复**。若怀疑数据本身有价值，
--   先 `CREATE TABLE request_abandoned_bak AS SELECT * FROM
--   request_abandoned;` 再回滚。
--
-- 列序/列数: 无（新建表，DROP TABLE）。级联面：**无**（不引用任何外键，
--   不被任何视图/物化视图引用——它是独立小表，不进任何 canonical 视图链）。
-- ===========================================================================
BEGIN;

-- 先确认表确实在。表不存在时直接 NOTICE 早退，而不是让 DROP 报
-- "relation does not exist"——那会把一次幂等回滚变成一次失败。
DO $$
BEGIN
  IF to_regclass('public.request_abandoned') IS NULL THEN
    RAISE NOTICE '819 down: public.request_abandoned absent; nothing to roll back';
    RETURN;
  END IF;
  RAISE NOTICE '819 down: dropping public.request_abandoned (its rows are unrecoverable — see header)';
END $$;

DO $$
BEGIN
  IF to_regclass('public.request_abandoned') IS NOT NULL THEN
    EXECUTE 'DROP TABLE IF EXISTS public.request_abandoned';
  END IF;
END $$;

-- 守卫：回滚必须真的收敛。DROP 之后表还在 = 有人挂了依赖对象，
-- 那种情况下「down 跑过了」这句话是假的，账本行也必须留着。
DO $$
BEGIN
  IF to_regclass('public.request_abandoned') IS NOT NULL THEN
    RAISE EXCEPTION '819 down: request_abandoned still present after DROP; aborting (a dependent object blocks it)';
  END IF;
  RAISE NOTICE '819 down: converged (public.request_abandoned is gone)';
END $$;

-- ledger 只在**真的回滚了**时才删。表本来就不存在时（上面早退的那条路径）
-- 不能删账本行——那会声称「819 没跑过」，而实际是**已经回滚过**。
DO $$
BEGIN
  IF to_regclass('public.request_abandoned') IS NOT NULL THEN
    RAISE NOTICE '819 down: table still present; keeping the ledger row (rollback did not converge)';
    RETURN;
  END IF;
  DELETE FROM public.schema_migrations WHERE version = '819';
  RAISE NOTICE '819 down: ledger row for 819 removed';
END $$;

COMMIT;
