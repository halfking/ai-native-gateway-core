-- 578_candidate_binding_scope_filter_disabled.sql
-- Filter disabled providers and manually-disabled credentials out of the
-- reorder scope hash.
--
-- Why this exists: the candidate-binding reorder endpoint (introduced in
-- migration 541, keyed by raw_model and extended by 569 to canonical_id)
-- defines its scope as the set of bindings an operator can see in the
-- /api/routing/resolve panel. That panel hides two classes of rows:
--
--   1. Bindings whose provider has p.enabled = FALSE
--      (admin/routing.go resolve query: `AND p.enabled IS TRUE`)
--   2. Bindings whose credential has c.manual_disabled = TRUE
--      (admins flip this on the credentials table; resolve surfaces the
--       credential as "credential_manual_disabled" only in the unavailable
--       reason column and never lists it as a routable row)
--
-- The scope row in candidate_binding_scope_revision(/_canonical) was
-- computed over ALL bindings for the (raw_model | canonical_id), including
-- the two hidden classes above. After an operator disabled a provider,
-- the dashboard computed the reorder hash from the visible rows while the
-- server still hashed the full set, and every subsequent drag PATCH
-- returned HTTP 409 ("stale candidate binding set") until the operator
-- refetched — a phantom drift bug that blocked dashboard sorting on any
-- model with a disabled provider.
--
-- admin/routing.go was patched (reorderScopeSQL / reorderScopeByCanonicalSQL
-- / ensureScopeRevision / ensureCanonicalScopeRevision) to skip those rows
-- at fetch time and at seed time. This migration patches the four
-- statement-level bump functions (541×3 + 569×3 plus the canonical pm_update
-- helper) so the persisted scope_hash converges to the same row set.
--
-- Without this fix, the dashboard resolves to "1:newHash", the writer reads
-- the new scope rows and computes the matching hash, and the persisted
-- "1:oldHash" still lives in the revision row — every reorder returns 409
-- even when the client's payload is structurally complete.
--
-- Scope of this migration: replace the four bump function bodies. No
-- table, trigger, or index changes. Trigger attachment from 541/569
-- remains valid; the predicate on update still fires on the same column
-- set, and the new JOINs add no new keys to advisory locking.
--
-- Requires: 541 (raw_model scope), 569 (canonical scope), 568 (priority
-- predicate), 571 (priority hash + canonical priority bump).
--
-- Idempotent: every CREATE OR REPLACE is safe to re-run.

BEGIN;

SET LOCAL lock_timeout = '5s';

DO $$
BEGIN
    IF to_regclass('public.candidate_binding_scope_revision') IS NULL THEN
        RAISE EXCEPTION '574_candidate_binding_scope_filter_disabled requires migration 541 (raw_model scope table missing)';
    END IF;
    IF to_regclass('public.candidate_binding_scope_revision_canonical') IS NULL THEN
        RAISE EXCEPTION '574_candidate_binding_scope_filter_disabled requires migration 569 (canonical scope table missing)';
    END IF;
END $$;

-- ── 1. Raw-model bump (insert) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          -- Mirror the resolve filter so the persisted hash matches what
          -- fetchReorderScope returns at write time.
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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

-- ── 2. Raw-model bump (delete) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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

-- ── 3. Raw-model bump (update) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.id = o.provider_model_id
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
          JOIN public.provider_models pm ON pm.raw_model_name = a.raw_model
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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
    'Statement trigger: bumps each affected raw_model scope once after binding INSERT (skips bindings under disabled providers and manually-disabled credentials since 578).';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_delete() IS
    'Statement trigger: bumps each affected raw_model scope once after binding DELETE (skips bindings under disabled providers and manually-disabled credentials since 578).';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_update() IS
    'Statement trigger: bumps each affected raw_model scope once after meaningful binding UPDATE (skips bindings under disabled providers and manually-disabled credentials since 578).';

-- ── 4. Canonical bump (insert) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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

-- ── 5. Canonical bump (delete) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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

-- ── 6. Canonical bump (update) — fold filter into the hash ───────────────
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
          JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
          JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
          JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
          JOIN public.credentials cr ON cr.id = b.credential_id
                                     AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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
    'Statement trigger: bumps each affected canonical_id scope once after binding INSERT (skips bindings under disabled providers and manually-disabled credentials since 574).';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_delete() IS
    'Statement trigger: bumps each affected canonical_id scope once after binding DELETE (skips bindings under disabled providers and manually-disabled credentials since 574).';
COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision_canonical_update() IS
    'Statement trigger: bumps each affected canonical_id scope once after meaningful binding UPDATE (skips bindings under disabled providers and manually-disabled credentials since 574).';

-- ── 7. provider_models.canonical_id-change bump — fold filter too ─────────
-- Same rationale as 569's pm_update: when an admin re-canonicalises a model,
-- the canonical scope membership changes, so the hash must include only the
-- filter-conformant rows. Without this fix the canonical_id remap path could
-- persist a hash computed over disabled-provider bindings.
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
              JOIN public.provider_models pm ON pm.canonical_id = a.canonical_id
              JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
              JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
              JOIN public.credentials cr ON cr.id = b.credential_id
                                         AND COALESCE(cr.manual_disabled, FALSE) = FALSE
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
    'Statement trigger: bumps both OLD and NEW canonical_id scopes on provider_models.canonical_id change (priority folded into hash since 571; filter-conformant rows since 574).';

-- ── 8. Recompute every existing scope row so the persisted hashes converge ─
-- Without this step, every previously-seeded scope row would carry a hash
-- computed over the full set including hidden rows, and clients hitting
-- /api/routing/resolve after the Go code deploys would see the new filtered
-- hash disagree with the persisted one (phantom 409s on first reorder
-- attempt until each scope happens to be bumped again). Run a single
-- ON CONFLICT DO UPDATE that recomputes each scope's hash with the new
-- filter applied, while leaving scope_version untouched so no client
-- sees a surprise version bump.
WITH raw_scopes AS (
    SELECT DISTINCT raw_model_name AS raw_model
      FROM public.provider_models
), raw_hashes AS (
    SELECT pm.raw_model_name AS raw_model,
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
      FROM raw_scopes s
      JOIN public.provider_models pm ON pm.raw_model_name = s.raw_model
      JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
      JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
      JOIN public.credentials cr ON cr.id = b.credential_id
                                 AND COALESCE(cr.manual_disabled, FALSE) = FALSE
     GROUP BY pm.raw_model_name
)
UPDATE public.candidate_binding_scope_revision r
   SET scope_hash = h.scope_hash
  FROM raw_hashes h
 WHERE r.raw_model = h.raw_model
   AND r.scope_hash IS DISTINCT FROM h.scope_hash;

WITH canonical_scopes AS (
    SELECT DISTINCT canonical_id
      FROM public.provider_models
     WHERE canonical_id IS NOT NULL
), canonical_hashes AS (
    SELECT pm.canonical_id,
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
      FROM canonical_scopes s
      JOIN public.provider_models pm ON pm.canonical_id = s.canonical_id
      JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
      JOIN public.providers pr ON pr.id = pm.provider_id AND pr.enabled = TRUE
      JOIN public.credentials cr ON cr.id = b.credential_id
                                 AND COALESCE(cr.manual_disabled, FALSE) = FALSE
     GROUP BY pm.canonical_id
)
UPDATE public.candidate_binding_scope_revision_canonical c
   SET scope_hash = h.scope_hash
  FROM canonical_hashes h
 WHERE c.canonical_id = h.canonical_id
   AND c.scope_hash IS DISTINCT FROM h.scope_hash;

COMMIT;
