-- Down migration for 831_work_type_route_source.sql.
-- Drops the source column and its index/constraint.
--
-- The column is dropped rather than reset: once it is gone, nothing
-- distinguishes an ACC-written row from an operator-written one again, so
-- re-applying the up migration after this would default every row to
-- 'operator' — including rows the next sync had legitimately written.
-- That is the safe direction (sync may no longer delete them), so the
-- down migration is still safe; it just cannot restore the distinction
-- for rows that existed after the column was added.
--
-- The schema_migrations ledger row is deleted (830 down convention).

BEGIN;

DROP INDEX IF EXISTS idx_wtmr_key_source;

ALTER TABLE work_type_model_route DROP CONSTRAINT IF EXISTS wtmr_source_check;
ALTER TABLE work_type_model_route DROP COLUMN IF EXISTS source;

DELETE FROM public.schema_migrations WHERE version = '831';

COMMIT;
