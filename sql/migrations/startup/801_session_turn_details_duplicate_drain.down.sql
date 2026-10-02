-- 801 deliberately has no automatic inverse.
--
-- Restoring the 733 promote function would reintroduce two proven defects:
-- duplicate hot rows already present in the parent never drain, and its
-- ON CONFLICT DO UPDATE path can replace parent-only enrichment with hot
-- NULLs. Dropping the function would stop hot retention altogether.
--
-- The 801 function keeps the same (interval, integer) signature and is
-- compatible with the older Go caller. During an application rollback,
-- retain this database function and its schema_migrations ledger entry.
-- If the function itself needs correction, audit the hot/parent conflicts
-- and ship a new forward migration. Do not delete or rewrite either tier
-- as part of an automatic down operation.

DO $rollback$
BEGIN
    RAISE EXCEPTION USING
        ERRCODE = 'P0001',
        MESSAGE = '759 automatic rollback refused: restoring 733 would strand hot duplicates and risk overwriting parent enrichment',
        HINT = 'Keep the 759 function during application rollback; audit conflicting hot rows and ship a new forward migration if the function needs correction.';
END
$rollback$;
