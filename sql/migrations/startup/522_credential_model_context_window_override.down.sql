-- Migration 522 down: revert credential_model_bindings context-window override
--
-- 先把视图恢复到无 context_window_override 列的定义，再删除三列（视图不再引用）。

BEGIN;

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
    pm.modality AS provider_modality
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));

ALTER TABLE public.credential_model_bindings DROP COLUMN IF EXISTS context_window_override;
ALTER TABLE public.credential_model_bindings DROP COLUMN IF EXISTS context_window_source;
ALTER TABLE public.credential_model_bindings DROP COLUMN IF EXISTS context_window_updated_at;

-- 恢复触发器为不含 context_window_override 的原始版本。
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
        updated_at = now()
    WHERE id = OLD.id;

    RETURN NEW;
END;
$$;

COMMIT;
