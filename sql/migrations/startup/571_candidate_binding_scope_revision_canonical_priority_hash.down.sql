-- 571_canonical_scope_revision_priority.down.sql
-- Restore the pre-571 canonical scope revision bump functions and recompute
-- scope_hash without priority.

BEGIN;

SET LOCAL lock_timeout = '5s';

-- ── 1. INSERT bump: drop priority from hash ──────────────────────────────
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

-- ── 2. DELETE bump: drop priority from hash ──────────────────────────────
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

-- ── 3. UPDATE bump: drop priority from hash AND predicate ────────────────
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

-- ── 4. provider_models.canonical_id-change bump: drop priority ───────────
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

-- ── 5. Recompute scope_hash without priority ─────────────────────────────
WITH hashes AS (
    SELECT r.canonical_id,
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
      FROM public.candidate_binding_scope_revision_canonical r
      LEFT JOIN public.provider_models pm ON pm.canonical_id = r.canonical_id
      LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
     GROUP BY r.canonical_id
)
UPDATE public.candidate_binding_scope_revision_canonical r
   SET scope_hash = h.scope_hash
  FROM hashes h
 WHERE h.canonical_id = r.canonical_id;

COMMIT;
