-- 357_repair_model_aliases_unique.sql
-- Enabler for migrations 352/354/355 which use
--   INSERT INTO model_aliases ... ON CONFLICT (canonical_id, raw_name) DO NOTHING
-- The target table had NO unique/exclusion constraint on (canonical_id, raw_name),
-- so those ON CONFLICT clauses failed at plan time ("no unique constraint matching
-- the ON CONFLICT specification"). Worse, the table also carried 237 groups of
-- exact-duplicate (canonical_id, raw_name) rows (all status='deprecated', identical
-- across every other column), which blocked adding the constraint outright.
--
-- Data corruption detail: model_aliases.id is NOT a unique primary key in this DB
-- (rows were bulk-inserted with repeated ids, e.g. four rows sharing id=117), so a
-- dedup keyed on `id` cannot collapse those groups. We therefore collapse physical
-- rows using ctid, keeping one row per (canonical_id, raw_name).
--
-- This migration:
--   1. Collapses exact duplicates to a single physical row per (canonical_id, raw_name)
--      (keeps one row via min(ctid); duplicate ids are not relied upon).
--   2. Adds the UNIQUE constraint the upstream migrations assume.
--
-- Idempotent: after the constraint exists and dups are gone, re-running keeps every
-- surviving row (each group has exactly one ctid) and the ADD CONSTRAINT is a no-op
-- only if the constraint already exists -- see down/up ordering notes in the report.
-- NOTE: the removed duplicate rows are lost; restore only via a pre-migration backup.

BEGIN;

DELETE FROM model_aliases a
WHERE a.ctid NOT IN (
    SELECT min(b.ctid) FROM model_aliases b GROUP BY b.canonical_id, b.raw_name
);

ALTER TABLE model_aliases
    ADD CONSTRAINT uq_model_aliases_canonical_raw
    UNIQUE (canonical_id, raw_name);

COMMIT;
