-- 541_candidate_binding_scope_revision.sql
-- Persistent monotonic version per raw_model scope for the reorder endpoint's
-- optimistic concurrency control.
--
-- Wire format: "<scope_version>:<scope_hash>" where scope_hash is the SHA-256 of
-- the complete scope's binding rows in id order (defence-in-depth secondary
-- check; the monotonic version alone is sufficient for 409 detection).
--
-- See admin/reorder_scope_revision/DESIGN.md for the full design.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('public.credential_model_bindings') IS NULL
       OR to_regclass('public.provider_models') IS NULL THEN
        RAISE EXCEPTION '541_candidate_binding_scope_revision requires credential_model_bindings and provider_models';
    END IF;
END $$;

-- ── 1. New table ─────────────────────────────────────────────────────────
-- raw_model is the routing scope used by admin/routing.go. It intentionally
-- has no FK: provider_models permits multiple providers for one raw_model_name
-- via UNIQUE (provider_id, raw_model_name), while this table aggregates those
-- provider rows into one revision scope.
CREATE TABLE IF NOT EXISTS public.candidate_binding_scope_revision (
    raw_model       text PRIMARY KEY,
    scope_version   bigint NOT NULL DEFAULT 1,
    scope_hash      char(64) NOT NULL DEFAULT '',
    last_bumped_at  timestamptz NOT NULL DEFAULT now(),
    last_bumped_by  text
);

COMMENT ON TABLE public.candidate_binding_scope_revision IS
    'Monotonic counter + complete-scope hash per raw_model, bumped atomically '
    'by priority / membership writes on credential_model_bindings. Consumed by '
    '/api/routing/candidate-bindings/reorder optimistic concurrency control.';

-- ── 2. Backfill every existing raw_model at version=1 ───────────────────
-- IMPORTANT: runs BEFORE the triggers are attached (step 4) so the backfill
-- does not double-bump. Empty scopes get an empty hash.
INSERT INTO public.candidate_binding_scope_revision (raw_model, scope_version, scope_hash)
SELECT
    pm.raw_model_name,
    1,
    COALESCE(
        encode(digest(string_agg(
            b.id::text || '|' ||
            b.credential_id::text || '|' ||
            b.manual_priority::text || '|' ||
            extract(epoch from b.updated_at)::text,
            '|' ORDER BY b.id
        ), 'sha256'), 'hex'),
        ''
    )
FROM public.provider_models pm
LEFT JOIN public.credential_model_bindings b
       ON b.provider_model_id = pm.id
GROUP BY pm.raw_model_name
ON CONFLICT (raw_model) DO NOTHING;

-- ── 3. Statement-level bump functions ────────────────────────────────────
-- A reorder writes the complete scope in one UPDATE statement. These functions
-- therefore use transition tables and recompute each affected scope once per
-- statement, instead of bumping once per binding row.
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    -- Order locks on candidate_binding_scope_revision by raw_model via
    -- advisory locks so concurrent statement-level triggers serialize
    -- predictably. The reorder handler already takes FOR UPDATE OF cmb on
    -- the underlying binding rows; this advisory lock extends that ordering
    -- to the revision upsert without touching those rows again.
    PERFORM pg_advisory_xact_lock(hashtextextended(s.raw_model, 0))
       FROM (
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM new_rows n
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          ORDER BY raw_model
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.raw_model_name AS raw_model
          FROM new_rows n
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
    ), hashes AS (
        SELECT a.raw_model,
               COALESCE(
                   encode(digest(string_agg(
                       b.id::text || '|' ||
                       b.credential_id::text || '|' ||
                       b.manual_priority::text || '|' ||
                       extract(epoch from b.updated_at)::text,
                       '|' ORDER BY b.id
                   ), 'sha256'), 'hex'),
                   ''
               ) AS scope_hash
          FROM affected a
          LEFT JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.raw_model
    )
    INSERT INTO public.candidate_binding_scope_revision
        (raw_model, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT raw_model, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (raw_model) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_delete()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(s.raw_model, 0))
       FROM (
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM old_rows o
           JOIN public.provider_models pm ON pm.id = o.provider_model_id
          ORDER BY raw_model
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.raw_model_name AS raw_model
          FROM old_rows o
          JOIN public.provider_models pm ON pm.id = o.provider_model_id
    ), hashes AS (
        SELECT a.raw_model,
               COALESCE(
                   encode(digest(string_agg(
                       b.id::text || '|' ||
                       b.credential_id::text || '|' ||
                       b.manual_priority::text || '|' ||
                       extract(epoch from b.updated_at)::text,
                       '|' ORDER BY b.id
                   ), 'sha256'), 'hex'),
                   ''
               ) AS scope_hash
          FROM affected a
          LEFT JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.raw_model
    )
    INSERT INTO public.candidate_binding_scope_revision
        (raw_model, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT raw_model, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (raw_model) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    -- Lock both the old and new raw_model in deterministic order so two
    -- concurrent reorder transactions cannot deadlock on the revision row
    -- when one moves a binding from scope A to scope B.
    PERFORM pg_advisory_xact_lock(hashtextextended(s.raw_model, 0))
       FROM (
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
         UNION
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = o.provider_model_id
          WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
          ORDER BY raw_model
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.raw_model_name AS raw_model
          FROM new_rows n
          JOIN old_rows o USING (id)
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
         WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
            OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
            OR o.credential_id IS DISTINCT FROM n.credential_id
        UNION
        SELECT DISTINCT pm.raw_model_name AS raw_model
          FROM new_rows n
          JOIN old_rows o USING (id)
          JOIN public.provider_models pm ON pm.id = o.provider_model_id
         WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
            OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
            OR o.credential_id IS DISTINCT FROM n.credential_id
    ), hashes AS (
        SELECT a.raw_model,
               COALESCE(
                   encode(digest(string_agg(
                       b.id::text || '|' ||
                       b.credential_id::text || '|' ||
                       b.manual_priority::text || '|' ||
                       extract(epoch from b.updated_at)::text,
                       '|' ORDER BY b.id
                   ), 'sha256'), 'hex'),
                   ''
               ) AS scope_hash
          FROM affected a
          LEFT JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.raw_model
    )
    INSERT INTO public.candidate_binding_scope_revision
        (raw_model, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT raw_model, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (raw_model) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_insert() IS
    'Statement trigger: bumps each affected raw_model scope once after binding INSERT.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_delete() IS
    'Statement trigger: bumps each affected raw_model scope once after binding DELETE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_update() IS
    'Statement trigger: bumps each affected raw_model scope once after meaningful binding UPDATE.';

-- ── 4. Attach statement-level triggers ───────────────────────────────────
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_insert ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_update ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_delete ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings;

CREATE TRIGGER trg_bump_cmb_scope_revision_insert
AFTER INSERT ON public.credential_model_bindings
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_insert();

CREATE TRIGGER trg_bump_cmb_scope_revision_update
AFTER UPDATE ON public.credential_model_bindings
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_update();

CREATE TRIGGER trg_bump_cmb_scope_revision_delete
AFTER DELETE ON public.credential_model_bindings
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_delete();

COMMIT;
