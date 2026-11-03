-- Down for 935: restore pre-935 two-arm view (10 cols), restore 698 promote body (10 cols),
-- drop hot RLS + owner indexes + the three columns on both arms.
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';
DROP VIEW IF EXISTS public.credit_ledger_with_current_month;
-- 恢复 935 前的 10 列旧定义（无 security_invoker）。
CREATE VIEW public.credit_ledger_with_current_month AS
 SELECT credit_ledger_hot.id,
    credit_ledger_hot.tenant_id,
    credit_ledger_hot.entry_type,
    credit_ledger_hot.amount,
    credit_ledger_hot.balance_after,
    credit_ledger_hot.ref_type,
    credit_ledger_hot.ref_id,
    credit_ledger_hot.note,
    credit_ledger_hot.created_at,
    credit_ledger_hot.pool
   FROM public.credit_ledger_hot
UNION ALL
 SELECT credit_ledger.id,
    credit_ledger.tenant_id,
    credit_ledger.entry_type,
    credit_ledger.amount,
    credit_ledger.balance_after,
    credit_ledger.ref_type,
    credit_ledger.ref_id,
    credit_ledger.note,
    credit_ledger.created_at,
    credit_ledger.pool
   FROM public.credit_ledger;

-- 恢复 698 版 promote 函数（列清单 10 列，逐字取自 698 迁移正文）。
CREATE OR REPLACE FUNCTION public.promote_credit_ledger_hot_to_partition(
  p_retention interval DEFAULT '8 hours'::interval,
  p_batch_size integer DEFAULT 5000
)
RETURNS bigint LANGUAGE plpgsql AS $$
DECLARE moved bigint := 0; month_rec record;
BEGIN
  SET LOCAL TIME ZONE 'Asia/Shanghai';
  IF p_retention IS NULL OR p_retention <= interval '0 seconds' THEN RAISE EXCEPTION 'p_retention must be positive'; END IF;
  IF p_batch_size IS NULL OR p_batch_size < 1 OR p_batch_size > 50000 THEN RAISE EXCEPTION 'p_batch_size must be between 1 and 50000'; END IF;
  FOR month_rec IN
    SELECT DISTINCT date_trunc('month', created_at) AS month_start
    FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY 1 LIMIT 12
  LOOP
    PERFORM public.ensure_credit_ledger_partition(month_rec.month_start);
  END LOOP;
  WITH batch AS (
    SELECT id FROM public.credit_ledger_hot
    WHERE created_at < now() - p_retention
    ORDER BY created_at, id LIMIT p_batch_size FOR UPDATE SKIP LOCKED
  ), moved_rows AS (
    DELETE FROM public.credit_ledger_hot h USING batch b
    WHERE h.id = b.id
    RETURNING h.id, h.tenant_id, h.entry_type, h.amount, h.balance_after,
      h.ref_type, h.ref_id, h.note, h.created_at, h.pool
  ), inserted AS (
    INSERT INTO public.credit_ledger (
      id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool)
    SELECT id, tenant_id, entry_type, amount, balance_after,
      ref_type, ref_id, note, created_at, pool
    FROM moved_rows
    RETURNING id
  ) SELECT count(*) INTO moved FROM inserted;
  RETURN moved;
END;
$$;

DROP POLICY IF EXISTS tenant_isolation_credit_ledger_hot ON public.credit_ledger_hot;
    -- ★ 2026-11-04 12h 审计轮：PostgreSQL **没有** `NO ROW LEVEL SECURITY` 这个子句
    --   （合法形态只有 ENABLE / DISABLE ROW LEVEL SECURITY）⇒ 原写法是**语法错误**
    --   ⇒ 整个 BEGIN/COMMIT 事务回滚 ⇒ 该 down 迁移**从未真正执行过**。
ALTER TABLE public.credit_ledger_hot DISABLE ROW LEVEL SECURITY;
DROP INDEX IF EXISTS public.idx_credit_ledger_hot_owner;
DROP INDEX IF EXISTS public.idx_credit_ledger_owner;
ALTER TABLE public.credit_ledger_hot
    DROP COLUMN IF EXISTS detail,
    DROP COLUMN IF EXISTS campaign_id,
    DROP COLUMN IF EXISTS owner_user;
ALTER TABLE public.credit_ledger
    DROP COLUMN IF EXISTS detail,
    DROP COLUMN IF EXISTS campaign_id,
    DROP COLUMN IF EXISTS owner_user;
DELETE FROM public.schema_migrations WHERE version = '935';
COMMIT;
