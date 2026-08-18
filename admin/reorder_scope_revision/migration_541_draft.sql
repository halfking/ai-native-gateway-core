-- Migration 541 (DRAFT): Persistent scope revision for reorder
--
-- Purpose:
--   Replace the per-response SHA-256 hash in admin/routing.go:764
--   candidateReorderRevision with a monotonic counter stored in
--   candidate_binding_scope_revision. The revision string the API
--   returns becomes "<scope_version>" (with optional "<hash>" suffix;
--   see DESIGN.md §3.3 for the wire format question).
--
-- Contract:
--   - The trigger fires once per real priority / membership change on
--     credential_model_bindings and advances scope_version by exactly 1.
--   - No-op UPDATEs (only updated_at changed) MUST NOT bump the counter,
--     so dashboards do not refetch every cache flush.
--   - The migration is zero-downtime: it adds the table, backfills, then
--     attaches the trigger. Old code keeps computing hashes; new code
--     reads the table.
--
-- Date: 2026-08-19
-- See: admin/reorder_scope_revision/DESIGN.md

BEGIN;

-- ── 1. New table ─────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS public.candidate_binding_scope_revision (
    raw_model       text PRIMARY KEY
                     REFERENCES public.provider_models(raw_model_name)
                     ON UPDATE CASCADE ON DELETE CASCADE,
    scope_version   bigint NOT NULL DEFAULT 1,
    scope_hash      char(64) NOT NULL DEFAULT '',
    last_bumped_at  timestamptz NOT NULL DEFAULT now(),
    last_bumped_by  text
);

COMMENT ON TABLE public.candidate_binding_scope_revision IS
    'Monotonic counter + hash per raw_model scope, bumped atomically by '
    'priority / membership writes on credential_model_bindings. Consumed by '
    '/api/routing/candidate-bindings/reorder optimistic concurrency control.';

-- ── 2. Backfill every existing raw_model ─────────────────────────────────
-- Insert at version 1 with the current SHA-256 of the scope. Empty scopes
-- (a raw_model with no bindings) get an empty hash, which the API
-- already represents as "no reorder available".
--
-- IMPORTANT: this INSERT runs BEFORE the trigger is attached (see step 4)
-- so the backfill does not double-bump.
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
        ), 'sha256'), 'sha256'),
        ''
    )
FROM public.provider_models pm
LEFT JOIN public.credential_model_bindings b
       ON b.provider_model_id = pm.id
GROUP BY pm.raw_model_name
ON CONFLICT (raw_model) DO NOTHING;

-- ── 3. Bump function ─────────────────────────────────────────────────────
-- Fires per row on INSERT / UPDATE / DELETE of credential_model_bindings.
-- Skips when only updated_at changed; otherwise resolves the raw_model,
-- recomputes the hash, and bumps scope_version by 1 in the same statement.
CREATE OR REPLACE FUNCTION public.bump_candidate_binding_scope_revision()
RETURNS TRIGGER AS $$
DECLARE
    v_raw_model text;
    v_priority  smallint;
    v_cred      bigint;
    v_id        bigint;
    v_updated   timestamptz;
    v_actor     text;
    v_new_hash  char(64);
BEGIN
    -- Pick the right row image and short-circuit on no-op UPDATEs.
    IF TG_OP = 'UPDATE' THEN
        IF OLD.manual_priority  = NEW.manual_priority
           AND OLD.provider_model_id = NEW.provider_model_id
           AND OLD.credential_id     = NEW.credential_id THEN
            RETURN NEW;
        END IF;
        v_id       := NEW.id;
        v_cred     := NEW.credential_id;
        v_priority := NEW.manual_priority;
        v_updated  := NEW.updated_at;
    ELSIF TG_OP = 'INSERT' THEN
        v_id       := NEW.id;
        v_cred     := NEW.credential_id;
        v_priority := NEW.manual_priority;
        v_updated  := NEW.updated_at;
    ELSIF TG_OP = 'DELETE' THEN
        v_id       := OLD.id;
        v_cred     := OLD.credential_id;
        v_priority := OLD.manual_priority;
        v_updated  := OLD.updated_at;
    END IF;

    SELECT pm.raw_model_name
      INTO v_raw_model
      FROM public.provider_models pm
     WHERE pm.id = COALESCE(NEW.provider_model_id, OLD.provider_model_id);

    IF v_raw_model IS NULL THEN
        RETURN COALESCE(NEW, OLD);
    END IF;

    v_new_hash := encode(digest(
        v_id::text || '|' ||
        v_cred::text || '|' ||
        v_priority::text || '|' ||
        extract(epoch from v_updated)::text,
        'sha256'), 'sha256');

    -- current_setting('app.actor', true) returns NULL when unset, which is
    -- exactly what we want for the legacy / direct-SQL backfill path.
    v_actor := current_setting('app.actor', true);

    INSERT INTO public.candidate_binding_scope_revision
        (raw_model, scope_version, scope_hash, last_bumped_at, last_bumped_by)
    VALUES
        (v_raw_model, 1, v_new_hash, now(), v_actor)
    ON CONFLICT (raw_model) DO UPDATE
        SET scope_version  = public.candidate_binding_scope_revision.scope_version + 1,
            scope_hash     = EXCLUDED.scope_hash,
            last_bumped_at = now(),
            last_bumped_by = EXCLUDED.last_bumped_by;

    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION public.bump_candidate_binding_scope_revision() IS
    'Per-row trigger function: bumps scope_version + recomputes scope_hash '
    'when credential_model_bindings.manual_priority / provider_model_id / '
    'credential_id change. Skips no-op UPDATEs (only updated_at changed).';

-- ── 4. Attach the trigger ────────────────────────────────────────────────
DROP TRIGGER IF EXISTS trg_bump_cmb_scope_revision ON public.credential_model_bindings;

CREATE TRIGGER trg_bump_cmb_scope_revision
AFTER INSERT OR UPDATE OF manual_priority, provider_model_id, credential_id
    OR DELETE
ON public.credential_model_bindings
FOR EACH ROW
EXECUTE FUNCTION public.bump_candidate_binding_scope_revision();

-- ── 5. last_bumped_by is set per transaction via:
--      SET LOCAL app.actor = '<actor>';  (already plumbed in admin/handler.go
--      via logAuditExec). For rows whose writer did not set the GUC,
--      the column stays NULL and we fall back to routing_audit_log joins.

COMMIT;
