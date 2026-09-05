-- 569_candidate_binding_scope_revision_canonical.sql
-- Persistent monotonic version per canonical_id scope for the reorder
-- endpoint's optimistic concurrency control.
--
-- Why a second scope table: the original 541 table is keyed by
-- provider_models.raw_model_name. A resolve that fans across multiple
-- raw_model_name variants of the same canonical model (e.g. glm-5.2 +
-- z-ai/glm-5.2, or kimi-k2-250711 + kimi-k2-250905) therefore spans
-- several 541 scopes, and the reorder UI disables dragging because it
-- cannot hold a single atomic revision across them.
--
-- This table aggregates EVERY binding that shares one canonical_id into a
-- single scope, so a drag operation can reorder all of a model's
-- candidates at once even when they are registered under multiple
-- raw_model_name aliases. Genuinely distinct canonical models (e.g.
-- glm-5-2-260617, canonical_id 192049) remain separate scopes — which is
-- correct: they are different models, not aliases.
--
-- Wire format is unchanged from 541: "<scope_version>:<scope_hash>" where
-- scope_hash is the SHA-256 of the complete scope's binding rows in id
-- order. See admin/routing.go and admin/reorder_scope_revision/DESIGN.md.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('public.credential_model_bindings') IS NULL
       OR to_regclass('public.provider_models') IS NULL THEN
        RAISE EXCEPTION '569_candidate_binding_scope_revision_canonical requires credential_model_bindings and provider_models';
    END IF;
END $$;

-- ── 1. New table ─────────────────────────────────────────────────────────
-- canonical_id is the routing scope used by admin/routing.go after this
-- change. It intentionally has no FK: provider_models permits multiple
-- providers / raw_model_name rows for one canonical_id, while this table
-- aggregates those provider rows into one revision scope.
CREATE TABLE IF NOT EXISTS public.candidate_binding_scope_revision_canonical (
    canonical_id    bigint PRIMARY KEY,
    scope_version   bigint NOT NULL DEFAULT 1,
    scope_hash      char(64) NOT NULL DEFAULT '',
    last_bumped_at  timestamptz NOT NULL DEFAULT now(),
    last_bumped_by  text
);

COMMENT ON TABLE public.candidate_binding_scope_revision_canonical IS
    'Monotonic counter + complete-scope hash per canonical_id, bumped '
    'atomically by priority / membership writes on credential_model_bindings. '
    'Groups bindings across all raw_model_name aliases of one canonical model. '
    'Consumed by /api/routing/candidate-bindings/reorder optimistic concurrency control.';

-- ── 2. Backfill every existing canonical_id at version=1 ─────────────────
-- IMPORTANT: runs BEFORE the triggers are attached (step 4) so the backfill
-- does not double-bump. Empty scopes get an empty hash.
INSERT INTO public.candidate_binding_scope_revision_canonical (canonical_id, scope_version, scope_hash)
SELECT
    pm.canonical_id,
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
WHERE pm.canonical_id IS NOT NULL
GROUP BY pm.canonical_id
ON CONFLICT (canonical_id) DO NOTHING;

-- ── 3. Statement-level bump functions ────────────────────────────────────
-- A reorder writes the complete scope in one UPDATE statement. These functions
-- therefore use transition tables and recompute each affected scope once per
-- statement, instead of bumping once per binding row.
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    -- Order locks on candidate_binding_scope_revision_canonical by canonical_id
    -- via advisory locks so concurrent statement-level triggers serialize
    -- predictably. The reorder handler already takes FOR UPDATE OF cmb on the
    -- underlying binding rows; this advisory lock extends that ordering to the
    -- revision upsert without touching those rows again.
    PERFORM pg_advisory_xact_lock(hashtextextended(s.canonical_id::text, 0))
       FROM (
         SELECT DISTINCT pm.canonical_id
           FROM new_rows n
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE pm.canonical_id IS NOT NULL
          ORDER BY pm.canonical_id
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.canonical_id
          FROM new_rows n
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
         WHERE pm.canonical_id IS NOT NULL
    ), hashes AS (
        SELECT a.canonical_id,
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
          LEFT JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.canonical_id
    )
    INSERT INTO public.candidate_binding_scope_revision_canonical
        (canonical_id, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT canonical_id, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (canonical_id) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision_canonical.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(s.canonical_id::text, 0))
       FROM (
         SELECT DISTINCT pm.canonical_id
           FROM old_rows o
           JOIN public.provider_models pm ON pm.id = o.provider_model_id
          WHERE pm.canonical_id IS NOT NULL
          ORDER BY pm.canonical_id
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.canonical_id
          FROM old_rows o
          JOIN public.provider_models pm ON pm.id = o.provider_model_id
         WHERE pm.canonical_id IS NOT NULL
    ), hashes AS (
        SELECT a.canonical_id,
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
          LEFT JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.canonical_id
    )
    INSERT INTO public.candidate_binding_scope_revision_canonical
        (canonical_id, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT canonical_id, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (canonical_id) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision_canonical.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    -- Lock both the old and new canonical_id in deterministic order so two
    -- concurrent reorder transactions cannot deadlock on the revision row
    -- when one moves a binding from scope A to scope B.
    PERFORM pg_advisory_xact_lock(hashtextextended(s.canonical_id::text, 0))
       FROM (
         SELECT DISTINCT pm.canonical_id
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE (
                o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
          )
            AND pm.canonical_id IS NOT NULL
         UNION
         SELECT DISTINCT pm.canonical_id
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = o.provider_model_id
          WHERE (
                o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
          )
            AND pm.canonical_id IS NOT NULL
          ORDER BY canonical_id
       ) s;
    v_actor := current_setting('app.actor', true);

    WITH affected AS (
        SELECT DISTINCT pm.canonical_id
          FROM new_rows n
          JOIN old_rows o USING (id)
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
         WHERE (
               o.manual_priority IS DISTINCT FROM n.manual_priority
            OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
            OR o.credential_id     IS DISTINCT FROM n.credential_id
         )
           AND pm.canonical_id IS NOT NULL
        UNION
        SELECT DISTINCT pm.canonical_id
          FROM new_rows n
          JOIN old_rows o USING (id)
          JOIN public.provider_models pm ON pm.id = o.provider_model_id
         WHERE (
               o.manual_priority IS DISTINCT FROM n.manual_priority
            OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
            OR o.credential_id     IS DISTINCT FROM n.credential_id
         )
           AND pm.canonical_id IS NOT NULL
    ), hashes AS (
        SELECT a.canonical_id,
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
          LEFT JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
         GROUP BY a.canonical_id
    )
    INSERT INTO public.candidate_binding_scope_revision_canonical
        (canonical_id, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    SELECT canonical_id, 1, scope_hash, now(), v_actor
      FROM hashes
    ON CONFLICT (canonical_id) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision_canonical.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = EXCLUDED.last_bumped_at,
            last_bumped_by = EXCLUDED.last_bumped_by;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert() IS
    'Statement trigger: bumps each affected canonical_id scope once after binding INSERT.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete() IS
    'Statement trigger: bumps each affected canonical_id scope once after binding DELETE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update() IS
    'Statement trigger: bumps each affected canonical_id scope once after meaningful binding UPDATE.';

-- ── 4. Attach statement-level triggers ───────────────────────────────────
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_insert ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_update ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_delete ON public.credential_model_bindings;
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical ON public.credential_model_bindings;

CREATE TRIGGER trg_bump_cmb_scope_revision_canonical_insert
AFTER INSERT ON public.credential_model_bindings
REFERENCING NEW TABLE AS new_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert();

CREATE TRIGGER trg_bump_cmb_scope_revision_canonical_update
AFTER UPDATE ON public.credential_model_bindings
REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update();

CREATE TRIGGER trg_bump_cmb_scope_revision_canonical_delete
AFTER DELETE ON public.credential_model_bindings
REFERENCING OLD TABLE AS old_rows
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete();

-- ── 5. provider_models.canonical_id change also bumps the scope ─────────
-- If an admin re-canonicalizes a model (e.g. re-maps a provider model to a
-- different canonical row), the canonical scope membership changes; the
-- binding-row triggers above do not fire, so we add a provider_models
-- update trigger that bumps both the OLD and the NEW canonical scopes.
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    IF NEW.canonical_id IS DISTINCT FROM OLD.canonical_id THEN
        v_actor := current_setting('app.actor', true);
        WITH affected AS (
            SELECT DISTINCT canonical_id FROM (
                SELECT OLD.canonical_id AS canonical_id
                WHERE OLD.canonical_id IS NOT NULL
                UNION
                SELECT NEW.canonical_id AS canonical_id
                WHERE NEW.canonical_id IS NOT NULL
            ) s
        ), hashes AS (
            SELECT a.canonical_id,
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
              LEFT JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
              LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
             GROUP BY a.canonical_id
        )
        INSERT INTO public.candidate_binding_scope_revision_canonical
            (canonical_id, scope_version, scope_hash, last_bumped_at, last_bumped_by)
        SELECT canonical_id, 1, scope_hash, now(), v_actor
          FROM hashes
        ON CONFLICT (canonical_id) DO UPDATE
            SET scope_version  = public.candidate_binding_scope_revision_canonical.scope_version + 1,
                scope_hash     = EXCLUDED.scope_hash,
                last_bumped_at = EXCLUDED.last_bumped_at,
                last_bumped_by = EXCLUDED.last_bumped_by;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision_canonical_pm_update ON public.provider_models;
CREATE TRIGGER trg_bump_cmb_scope_revision_canonical_pm_update
AFTER UPDATE OF canonical_id ON public.provider_models
FOR EACH STATEMENT
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update();

COMMIT;
