-- 571_candidate_binding_scope_revision_canonical_priority_hash.sql
-- Include per-binding priority in the canonical scope revision hash.
--
-- Why this exists: migration 569 created the per-canonical_id scope used by
-- /api/routing/candidate-bindings/reorder optimistic concurrency control, but
-- the scope hash formula only covered (id, credential_id, manual_priority,
-- updated_at). Migration 568 added credential_model_bindings.priority and
-- patched the per-raw_model scope (541) to fold priority into the hash and
-- into the UPDATE trigger predicate; this migration extends the same fix to
-- the per-canonical_id scope (569) so a priority flip invalidates the canonical
-- reorder revision.
--
-- Without this fix, two admins on different tabs can each:
--   1. call resolve, see reorder_revision = "1:abc..."
--   2. one flips priority=true on credential_id=A
--   3. one submits a reorder with the old revision, which the trigger accepts
--      because the canonical trigger never fired on the priority change
--   4. the other tab now sees a stale scope_hash while priority has shifted
--      underneath it, defeating the OCC contract
--
-- Scope: this migration requires migration 569 (canonical scope exists) and
-- migration 568 (cmb.priority exists). It only patches the four
-- CREATE OR REPLACE FUNCTION bodies; no table, trigger, or index changes.
-- Triggers from 569 remain valid; the bump functions now recognise priority
-- as a scope input.
--
-- Idempotent: every CREATE OR REPLACE is safe to re-run.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('public.candidate_binding_scope_revision_canonical') IS NULL THEN
        RAISE EXCEPTION '571_candidate_binding_scope_revision_canonical_priority_hash requires migration 569 (canonical scope table missing)';
    END IF;
    IF NOT EXISTS (
        SELECT 1
          FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name = 'credential_model_bindings'
           AND column_name = 'priority'
    ) THEN
        RAISE EXCEPTION '571_candidate_binding_scope_revision_canonical_priority_hash requires migration 568 (credential_model_bindings.priority missing)';
    END IF;
END $$;

-- ── 1. Replace the INSERT bump to fold priority into the hash ────────────
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
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
                       b.priority::text || '|' ||
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

-- ── 2. Replace the DELETE bump to fold priority into the hash ────────────
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
                       b.priority::text || '|' ||
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

-- ── 3. Replace the UPDATE bump: priority in hash AND in predicate ────────
-- The predicate now treats a priority flip as scope-affecting. Without
-- adding OR o.priority IS DISTINCT FROM n.priority the trigger would only
-- fire on manual_priority / provider_model_id / credential_id changes and
-- silently allow priority to drift inside an OCC-locked scope.
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_canonical_update()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(s.canonical_id::text, 0))
       FROM (
         SELECT DISTINCT pm.canonical_id
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE (
                o.manual_priority  IS DISTINCT FROM n.manual_priority
             OR o.priority          IS DISTINCT FROM n.priority
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
                o.manual_priority  IS DISTINCT FROM n.manual_priority
             OR o.priority          IS DISTINCT FROM n.priority
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
               o.manual_priority  IS DISTINCT FROM n.manual_priority
            OR o.priority          IS DISTINCT FROM n.priority
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
               o.manual_priority  IS DISTINCT FROM n.manual_priority
            OR o.priority          IS DISTINCT FROM n.priority
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
                       b.priority::text || '|' ||
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

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update() IS
    'Statement trigger: bumps each affected canonical_id scope once after meaningful binding UPDATE (priority flip counted as scope-affecting since 571).';

-- ── 4. Replace the provider_models.canonical_id-change bump ──────────────
-- The canonical_id remap path also folds priority into the recomputed hash
-- so a remap that surfaces priority changes still invalidates the scope.
-- The trigger itself only fires on NEW.canonical_id IS DISTINCT FROM
-- OLD.canonical_id, so the predicate does not need a priority clause.
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
                           b.priority::text || '|' ||
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

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update() IS
    'Statement trigger: bumps both OLD and NEW canonical_id scopes on provider_models.canonical_id change (priority folded into hash since 571).';

COMMIT;
