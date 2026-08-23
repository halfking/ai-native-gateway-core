-- 561_credential_priority_flag.sql
-- Add per credential x model priority routing and include it in routing cache invalidation.
--
-- Idempotent: the column is guarded by information_schema and all dependent
-- objects are recreated with CREATE OR REPLACE or DROP + CREATE.

BEGIN;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
          FROM information_schema.columns
         WHERE table_schema = 'public'
           AND table_name = 'credential_model_bindings'
           AND column_name = 'priority'
    ) THEN
        ALTER TABLE public.credential_model_bindings
            ADD COLUMN priority boolean NOT NULL DEFAULT false;
    END IF;
END $$;

COMMENT ON COLUMN public.credential_model_bindings.priority IS
    'Per credential x model routing priority. When quota_state is ok, priority bindings receive traffic first.';

-- Recompute existing hashes without bumping scope versions so the persisted
-- revision state uses the same complete-row format as the trigger functions.
WITH hashes AS (
    SELECT r.raw_model,
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
      FROM public.candidate_binding_scope_revision r
      LEFT JOIN public.provider_models pm ON pm.raw_model_name = r.raw_model
      LEFT JOIN public.credential_model_bindings b ON b.provider_model_id = pm.id
     GROUP BY r.raw_model
)
UPDATE public.candidate_binding_scope_revision r
   SET scope_hash = h.scope_hash
  FROM hashes h
 WHERE h.raw_model = r.raw_model;

-- Expose the per-binding priority to routing reads and view updates.
CREATE OR REPLACE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
    pm.canonical_raw_name,
    pm.raw_model_name,
    cmb.success_rate,
    cmb.p95_latency_ms,
    cmb.available,
    pm.last_seen_at,
    cmb.routing_tier,
    cmb.weight,
    cmb.unit_price_in_per_1m,
    cmb.unit_price_out_per_1m,
    cmb.currency,
    pm.outbound_model_name,
    cmb.cache_read_price_per_1m,
    cmb.cache_write_price_per_1m,
    pm.standardized_name,
    cmb.unavailable_reason,
    cmb.unavailable_at,
    cmb.unavailable_recover_at,
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected,
    cmb.created_at,
    cmb.updated_at,
    pm.modality AS provider_modality,
    cmb.context_window_override,
    cmb.priority
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));

-- Let INSTEAD OF UPDATE on model_offers persist the new binding flag.
CREATE OR REPLACE FUNCTION public.model_offers_update_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    v_pm_id BIGINT;
BEGIN
    SELECT provider_model_id INTO v_pm_id
    FROM credential_model_bindings WHERE id = OLD.id;

    IF v_pm_id IS NOT NULL THEN
        UPDATE provider_models SET
            canonical_id = COALESCE(NEW.canonical_id, provider_models.canonical_id),
            standardized_name = COALESCE(NEW.standardized_name, provider_models.standardized_name),
            outbound_model_name = COALESCE(NEW.outbound_model_name, provider_models.outbound_model_name),
            last_seen_at = COALESCE(NEW.last_seen_at, provider_models.last_seen_at),
            updated_at = now()
        WHERE id = v_pm_id;
    END IF;

    UPDATE credential_model_bindings SET
        available = COALESCE(NEW.available, credential_model_bindings.available),
        unavailable_reason = CASE
            WHEN NEW.unavailable_reason IS NOT NULL THEN NEW.unavailable_reason
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_reason
        END,
        unavailable_at = CASE
            WHEN NEW.unavailable_at IS NOT NULL THEN NEW.unavailable_at
            WHEN NEW.available IS NOT NULL AND NEW.available = TRUE THEN NULL
            ELSE credential_model_bindings.unavailable_at
        END,
        admin_protected = CASE
            WHEN NEW.admin_protected IS NOT NULL THEN NEW.admin_protected
            ELSE credential_model_bindings.admin_protected
        END,
        routing_tier = COALESCE(NEW.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(NEW.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(NEW.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(NEW.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(NEW.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(NEW.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(NEW.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(NEW.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(NEW.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(NEW.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(NEW.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(NEW.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(NEW.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(NEW.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(NEW.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        context_window_override = COALESCE(NEW.context_window_override, credential_model_bindings.context_window_override),
        priority = COALESCE(NEW.priority, credential_model_bindings.priority),
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

-- Recreate the view triggers after the update trigger function exists.
DROP TRIGGER IF EXISTS model_offers_insert ON public.model_offers;
DROP TRIGGER IF EXISTS model_offers_update ON public.model_offers;
DROP TRIGGER IF EXISTS model_offers_delete ON public.model_offers;
CREATE TRIGGER model_offers_insert INSTEAD OF INSERT ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_insert_trigger();
CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_update_trigger();
CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_delete_trigger();

-- Refresh routing caches when this per-binding routing input changes.
DROP TRIGGER IF EXISTS trg_notify_auto_route_cmb_update ON public.credential_model_bindings;

CREATE TRIGGER trg_notify_auto_route_cmb_update
    AFTER UPDATE ON public.credential_model_bindings
    FOR EACH ROW
    WHEN (
        old.available IS DISTINCT FROM new.available
        OR old.unavailable_reason IS DISTINCT FROM new.unavailable_reason
        OR old.unavailable_at IS DISTINCT FROM new.unavailable_at
        OR old.routing_tier IS DISTINCT FROM new.routing_tier
        OR old.weight IS DISTINCT FROM new.weight
        OR old.manual_priority IS DISTINCT FROM new.manual_priority
        OR old.active_sessions IS DISTINCT FROM new.active_sessions
        OR old.consecutive_failures IS DISTINCT FROM new.consecutive_failures
        OR old.context_window_override IS DISTINCT FROM new.context_window_override
        OR old.priority IS DISTINCT FROM new.priority
    )
    EXECUTE FUNCTION public.notify_auto_route_refresh();

-- Include priority in every complete-scope hash and treat changes to it as
-- scope-affecting updates.
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

COMMIT;
