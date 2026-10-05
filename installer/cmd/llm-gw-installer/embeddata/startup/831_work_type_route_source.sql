-- Migration 831: record which writer owns each work_type model route.
--
-- Why this exists
-- ---------------
-- Two writers replace the full route set for a work type key:
--
--   * admin/acc_work_types.go  syncWorkTypesFromACC (the ACC sync)
--   * admin/work_types.go      putRoutes (the admin UI)
--
-- Both did `DELETE FROM work_type_model_route WHERE work_type_key = $1`
-- followed by re-INSERT, and neither recorded who wrote the row. So the
-- two writers clobber each other with no way to tell them apart.
--
-- The consequence is data loss, not just contention. The ACC seed's
-- `model_routes` is empty for all 22 entries (ACC's WorkType.ModelRoutes
-- is []string{} on every row), so a successful sync deletes every route
-- for a key and reinserts zero. An operator who configured model routes
-- through the admin UI loses them, silently: the sync result only reports
-- "0 routes", and there is no WARN.
--
-- Today that loss is masked by a bug: the gateway requests
-- /api/llm/work-types while acc-go serves /api/v2/llm/work-types, so the
-- sync 404s and never runs. Fixing the path without fixing this would
-- turn a harmless 404 into a periodic silent wipe, which is why the path
-- fix and this migration must land together.
--
-- What it does
-- -----------
-- Adds work_type_model_route.source ∈ {operator, acc}, defaulting to
-- 'operator'. The default is the correct backfill value rather than a
-- convenience: no ACC sync has ever completed, so every existing row was
-- written by the admin UI. Marking them 'acc' would hand the next sync
-- permission to delete them.
--
-- The sync then deletes only its own rows and inserts with
-- ON CONFLICT DO NOTHING, so an operator's route for a given model always
-- wins. This is the 491 convention ("operator-edited route sets are never
-- reverted on re-run") extended from the seed path to the sync path.
-- putRoutes deliberately keeps its full-replace delete: an operator
-- editing a key in the UI is claiming it, and being unable to remove a
-- synced row would be worse than losing it.
--
-- Idempotent, safe to re-run. Down migration removes the column.

BEGIN;

ALTER TABLE work_type_model_route
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'operator';

-- Belt-and-braces for any row that predates the default on a table that
-- was altered by an older copy of this file.
UPDATE work_type_model_route SET source = 'operator' WHERE source IS NULL OR source = '';

ALTER TABLE work_type_model_route DROP CONSTRAINT IF EXISTS wtmr_source_check;
ALTER TABLE work_type_model_route
    ADD CONSTRAINT wtmr_source_check CHECK (source IN ('operator', 'acc'));

-- The sync's per-key filter is `source = 'acc'`; without this index every
-- sync runs a sequential scan per key.
CREATE INDEX IF NOT EXISTS idx_wtmr_key_source
    ON work_type_model_route (work_type_key, source);

COMMENT ON COLUMN work_type_model_route.source IS
    'Which writer last wrote this row. The ACC sync only deletes its own (acc) rows; operator rows are never removed by a sync.';

-- 双账本自登记(695/701/703/704/709 定式)。幂等:重跑安全。
INSERT INTO public.schema_migrations (version, description)
VALUES ('831', 'work_type_model_route.source: separate ACC-owned routes from operator-owned so sync stops wiping operator config')
ON CONFLICT (version) DO UPDATE SET description = EXCLUDED.description;

COMMIT;
