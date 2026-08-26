--
-- Name: model_offers_insert_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_insert_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    INSERT INTO provider_models (provider_id, raw_model_name, canonical_id, outbound_model_name, available, last_seen_at)
    VALUES (
        (SELECT provider_id FROM credentials WHERE id = NEW.credential_id),
        NEW.raw_model_name,
        NEW.canonical_id,
        NEW.outbound_model_name,
        COALESCE(NEW.available, TRUE),
        COALESCE(NEW.last_seen_at, now())
    )
    ON CONFLICT (provider_id, raw_model_name) DO UPDATE SET
        canonical_id = COALESCE(EXCLUDED.canonical_id, provider_models.canonical_id),
        outbound_model_name = COALESCE(EXCLUDED.outbound_model_name, provider_models.outbound_model_name),
        last_seen_at = COALESCE(EXCLUDED.last_seen_at, provider_models.last_seen_at),
        available = TRUE,
        updated_at = now()
    RETURNING id INTO NEW.id;

    INSERT INTO credential_model_bindings (
        credential_id, provider_model_id, available,
        routing_tier, weight, manual_priority,
        success_rate, p95_latency_ms, active_sessions, consecutive_failures,
        unit_price_in_per_1m, unit_price_out_per_1m,
        cache_read_price_per_1m, cache_write_price_per_1m,
        currency, billing_mode, pricing_source, pricing_updated_at,
        admin_protected
    ) VALUES (
        NEW.credential_id, NEW.id, COALESCE(NEW.available, TRUE),
        COALESCE(NEW.routing_tier, 2), COALESCE(NEW.weight, 100), COALESCE(NEW.manual_priority, 99),
        COALESCE(NEW.success_rate, 0.9), COALESCE(NEW.p95_latency_ms, 0),
        COALESCE(NEW.active_sessions, 0), COALESCE(NEW.consecutive_failures, 0),
        COALESCE(NEW.unit_price_in_per_1m, 0), COALESCE(NEW.unit_price_out_per_1m, 0),
        COALESCE(NEW.cache_read_price_per_1m, 0), COALESCE(NEW.cache_write_price_per_1m, 0),
        COALESCE(NEW.currency, 'USD'), COALESCE(NEW.billing_mode, 'token'),
        NEW.pricing_source, NEW.pricing_updated_at,
        COALESCE(NEW.admin_protected, FALSE)
    )
    ON CONFLICT (credential_id, provider_model_id) DO UPDATE SET
        routing_tier = COALESCE(EXCLUDED.routing_tier, credential_model_bindings.routing_tier),
        weight = COALESCE(EXCLUDED.weight, credential_model_bindings.weight),
        manual_priority = COALESCE(EXCLUDED.manual_priority, credential_model_bindings.manual_priority),
        success_rate = COALESCE(EXCLUDED.success_rate, credential_model_bindings.success_rate),
        p95_latency_ms = COALESCE(EXCLUDED.p95_latency_ms, credential_model_bindings.p95_latency_ms),
        active_sessions = COALESCE(EXCLUDED.active_sessions, credential_model_bindings.active_sessions),
        consecutive_failures = COALESCE(EXCLUDED.consecutive_failures, credential_model_bindings.consecutive_failures),
        unit_price_in_per_1m = COALESCE(EXCLUDED.unit_price_in_per_1m, credential_model_bindings.unit_price_in_per_1m),
        unit_price_out_per_1m = COALESCE(EXCLUDED.unit_price_out_per_1m, credential_model_bindings.unit_price_out_per_1m),
        cache_read_price_per_1m = COALESCE(EXCLUDED.cache_read_price_per_1m, credential_model_bindings.cache_read_price_per_1m),
        cache_write_price_per_1m = COALESCE(EXCLUDED.cache_write_price_per_1m, credential_model_bindings.cache_write_price_per_1m),
        currency = COALESCE(EXCLUDED.currency, credential_model_bindings.currency),
        billing_mode = COALESCE(EXCLUDED.billing_mode, credential_model_bindings.billing_mode),
        pricing_source = COALESCE(EXCLUDED.pricing_source, credential_model_bindings.pricing_source),
        pricing_updated_at = COALESCE(EXCLUDED.pricing_updated_at, credential_model_bindings.pricing_updated_at),
        updated_at = now();

    RETURN NEW;
END;
$$;

