-- 574_candidate_binding_scope_filter_disabled.down.sql
-- Revert bump functions to the pre-574 hash formula (no filter on
-- providers.enabled or credentials.manual_disabled). Does NOT recompute
-- the persisted scope_hash; clients will see a one-time 409 drift on the
-- first reorder attempt after rollback, identical to the rollback
-- behaviour of 571 (priority hash). The hash self-heals on the next binding
-- write to each scope.
--
-- The Go-level filters added in the same change (admin/routing.go
-- reorderScopeSQL / reorderScopeByCanonicalSQL / ensure helpers) are
-- reverted by the corresponding code revert; this SQL only restores the
-- trigger function bodies.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('public.candidate_binding_scope_revision') IS NULL THEN
        RAISE EXCEPTION '574_candidate_binding_scope_filter_disabled.down requires migration 541';
    END IF;
    IF to_regclass('public.candidate_binding_scope_revision_canonical') IS NULL THEN
        RAISE EXCEPTION '574_candidate_binding_scope_filter_disabled.down requires migration 569';
    END IF;
END $$;

-- Restore the pre-574 insert bump (raw_model scope).
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_insert()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
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
                       b.priority::text || '|' ||
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

-- Restore the pre-574 delete bump (raw_model scope).
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
                       b.priority::text || '|' ||
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

-- Restore the pre-574 update bump (raw_model scope).
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision_update()
RETURNS TRIGGER AS $$
DECLARE
    v_actor text;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(s.raw_model, 0))
       FROM (
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.priority        IS DISTINCT FROM n.priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
         UNION
         SELECT DISTINCT pm.raw_model_name AS raw_model
           FROM new_rows n
           JOIN old_rows o USING (id)
           JOIN public.provider_models pm ON pm.id = o.provider_model_id
          WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.priority        IS DISTINCT FROM n.priority
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
             OR o.priority        IS DISTINCT FROM n.priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
        UNION
        SELECT DISTINCT pm.raw_model_name AS raw_model
          FROM new_rows n
          JOIN old_rows o USING (id)
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
          WHERE o.manual_priority IS DISTINCT FROM n.manual_priority
             OR o.priority        IS DISTINCT FROM n.priority
             OR o.provider_model_id IS DISTINCT FROM n.provider_model_id
             OR o.credential_id     IS DISTINCT FROM n.credential_id
    ), hashes AS (
        SELECT a.raw_model,
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

-- Restore the pre-574 insert bump (canonical scope).
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

-- Restore the pre-574 delete bump (canonical scope).
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

-- Restore the pre-574 update bump (canonical scope).
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
          JOIN public.provider_models pm ON pm.id = n.provider_model_id
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

-- Restore the pre-574 provider_models.canonical_id-change bump.
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

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_insert() IS
    'Statement trigger: bumps each affected raw_model scope once after binding INSERT.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_delete() IS
    'Statement trigger: bumps each affected raw_model scope once after binding DELETE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_update() IS
    'Statement trigger: bumps each affected raw_model scope once after meaningful binding UPDATE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_insert() IS
    'Statement trigger: bumps each affected canonical_id scope once after binding INSERT.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete() IS
    'Statement trigger: bumps each affected canonical_id scope once after binding DELETE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update() IS
    'Statement trigger: bumps each affected canonical_id scope once after meaningful binding UPDATE.';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_pm_update() IS
    'Statement trigger: bumps both OLD and NEW canonical_id scopes on provider_models.canonical_id change.';

COMMIT;
