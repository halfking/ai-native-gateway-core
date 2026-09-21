-- 704 down: remove the plan-quota probe failure bookkeeping column.
-- The column only records "last failed plan probe" for sweep backoff
-- (R28 audit #12a); dropping it reverts to the pre-704 behavior where
-- failing credentials keep their position at the head of the sweep window.
-- The schema_migrations ledger row is intentionally kept (append-only
-- ledger, mirrors 701/703 down convention of leaving stamps in place).

ALTER TABLE public.credentials DROP COLUMN IF EXISTS plan_quota_probe_failed_at;
