-- =============================================================================
-- 01-schema.sql — Full public schema for llm_gateway database
-- =============================================================================
-- Reverse-engineered from production DB on 2026-06-24 using:
--   pg_dump --schema-only --no-owner --no-privileges --no-tablespaces \
--           --exclude-schema='_timescaledb*' --exclude-schema='timescaledb_*' \
--           --schema=public
--
-- Regenerate with: ./dump-schema.sh
--
-- Run order: AFTER 00-prereqs.sql, BEFORE 02-seed.sql. Idempotent: every
-- CREATE/ALTER uses IF NOT EXISTS, safe to re-run.
--
-- Post-processing: dump-schema.sh does two re-orderings because pg_dump
-- orders by OID (creation order), not by dependency:
--   1. recent_success_rate (LANGUAGE sql, refs request_logs) — moved to a
--      "DEFERRED FUNCTIONS" block placed BEFORE the first CREATE TRIGGER.
--   2. trigger functions (e.g. cmb_protect_manual_disable, routing_overrides_audit_fn)
--      — also need to live in the DEFERRED block because PostgreSQL validates
--      the function exists at CREATE TRIGGER time.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS public;


--
-- Name: SCHEMA public; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON SCHEMA public IS 'standard public schema';


--
-- Name: auto_set_fp_slot_limit(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.auto_set_fp_slot_limit() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    -- Auto-fill fp_slot_limit from concurrency_limit if not explicitly set
    IF NEW.fp_slot_limit IS NULL THEN
        IF NEW.concurrency_limit IS NOT NULL AND NEW.concurrency_limit > 0 THEN
            NEW.fp_slot_limit := GREATEST(1, NEW.concurrency_limit / 4);
        ELSE
            NEW.fp_slot_limit := 5;  -- safe fallback for NULL concurrency
        END IF;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: check_credential_dates(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.check_credential_dates() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF NEW.effective_at IS NOT NULL AND NEW.expires_at IS NOT NULL THEN
        IF NEW.expires_at <= NEW.effective_at THEN
            RAISE EXCEPTION 'expires_at must be greater than effective_at';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;


--
-- Name: ensure_request_logs_partition(timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.ensure_request_logs_partition(target_ts timestamp with time zone DEFAULT now()) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    month_start   date := date_trunc('month', target_ts)::date;
    month_end     date := (date_trunc('month', target_ts) + interval '1 month')::date;
    part_name     text := 'request_logs_' || to_char(month_start, 'YYYY_MM');
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_class WHERE relname = part_name) THEN
        EXECUTE format(
            'CREATE TABLE %I PARTITION OF request_logs FOR VALUES FROM (%L) TO (%L)',
            part_name, month_start, month_end
        );
        EXECUTE format(
            'CREATE INDEX idx_%s_search_trgm ON %I USING gin (search_text gin_trgm_ops)',
            part_name, part_name
        );
    END IF;
END;
$$;


--
-- Name: get_current_tenant(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_current_tenant() RETURNS text
    LANGUAGE sql STABLE
    AS $$ SELECT COALESCE(NULLIF(current_setting('app.current_tenant', true), ''), 'default'); $$;


--
-- Name: key_applications_set_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.key_applications_set_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;


--
-- Name: model_offers_delete_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_delete_trigger() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    UPDATE credential_model_bindings SET
        available = FALSE,
        unavailable_reason = 'deleted',
        admin_protected = FALSE,
        updated_at = now()
    WHERE id = OLD.id;
    RETURN OLD;
END;
$$;


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


--
-- Name: model_offers_update_trigger(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_offers_update_trigger() RETURNS trigger
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


--
-- Name: model_probe_backoff(integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff(consecutive_failures integer) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
		    SELECT CASE
			WHEN consecutive_failures <= 0 THEN INTERVAL '30 seconds'
			WHEN consecutive_failures = 1  THEN INTERVAL '2 minutes'
			WHEN consecutive_failures = 2  THEN INTERVAL '5 minutes'
			ELSE                                  INTERVAL '15 minutes'
		    END;
		$$;


--
-- Name: model_probe_backoff_v2(integer, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone) RETURNS interval
    LANGUAGE sql IMMUTABLE
    AS $$
    WITH age AS (
        SELECT EXTRACT(EPOCH FROM (NOW() - COALESCE(last_attempt_at, NOW() - INTERVAL '1 hour'))) AS secs
    )
    SELECT CASE
        -- 0 failures → healthy_confirmed watchdog (every 2h)
        WHEN consecutive_failures <= 0 THEN INTERVAL '2 hours'

        -- 3+ failures → still recovering toward broken_confirmed
        WHEN consecutive_failures >= 3 THEN INTERVAL '60 minutes'

        -- 1 failure: ramp up frequency when fresh, taper when stale
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <   300 THEN INTERVAL '1 minute'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '3 minutes'
        WHEN consecutive_failures = 1 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '10 minutes'
        WHEN consecutive_failures = 1                              THEN INTERVAL '30 minutes'

        -- 2 failures: same pattern but with longer floor
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <   300 THEN INTERVAL '2 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  1800 THEN INTERVAL '5 minutes'
        WHEN consecutive_failures = 2 AND (SELECT secs FROM age) <  3600 THEN INTERVAL '15 minutes'
        WHEN consecutive_failures = 2                              THEN INTERVAL '45 minutes'

        -- 4+ failures: very rare, treat like 3+
        ELSE INTERVAL '60 minutes'
    END;
$$;


--
-- Name: FUNCTION model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone); Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON FUNCTION public.model_probe_backoff_v2(consecutive_failures integer, last_attempt_at timestamp with time zone) IS 'Adaptive backoff: 0 fails = 2h watchdog; 1 fail ramps 1m→30m as the failure ages; 2 fails ramps 2m→45m; 3+ fails = 60m recovering.';


--
-- Name: model_probe_passive_boost(bigint, text, timestamp with time zone); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_passive_boost(p_credential_id bigint, p_raw_model_name text, p_now timestamp with time zone) RETURNS void
    LANGUAGE plpgsql
    AS $$
DECLARE
    recent_count INTEGER;
    new_retry TIMESTAMPTZ;
BEGIN
    SELECT COUNT(*) INTO recent_count
    FROM candidate_failure_logs
    WHERE credential_id = p_credential_id
      AND raw_model_name = p_raw_model_name
      AND ts > p_now - INTERVAL '5 minutes';

    IF recent_count >= 3 THEN
        new_retry := p_now + INTERVAL '30 seconds';
    ELSIF recent_count >= 2 THEN
        new_retry := p_now + INTERVAL '1 minute';
    ELSE
        RETURN;
    END IF;

    UPDATE model_probe_state mps
    SET next_retry_at = LEAST(COALESCE(mps.next_retry_at, new_retry), new_retry)
    WHERE mps.credential_id = p_credential_id
      AND mps.raw_model_name = p_raw_model_name
      AND COALESCE(mps.state, 'unknown') <> 'broken_confirmed';
END;
$$;


--
-- Name: notify_auto_route_refresh(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.notify_auto_route_refresh() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    entity_id text := '';
BEGIN
    IF TG_TABLE_NAME = 'credential_model_bindings' THEN
        entity_id := COALESCE(NEW.credential_id, OLD.credential_id)::text;
    ELSIF TG_TABLE_NAME IN ('credentials', 'api_keys') THEN
        entity_id := COALESCE(NEW.id, OLD.id)::text;
    END IF;

    PERFORM pg_notify('auto_route_refresh',
        TG_TABLE_NAME || ':' || TG_OP || entity_id);
    RETURN COALESCE(NEW, OLD);
END;
$$;


--
-- Name: recent_success_rate(bigint, text, integer, integer); Type: FUNCTION; Schema: public; Owner: -
--

-- -----------------------------------------------------------------------------
-- The function `recent_success_rate` was originally here in the pg_dump output,
-- but pg_dump orders by creation OID, not by dependency. This function references
-- `request_logs` directly (LANGUAGE sql) which doesn't exist yet at this point.
-- It has been moved to the end of this file, after all tables are created.
-- -----------------------------------------------------------------------------


--
-- Name: update_api_key_model_cost(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_api_key_model_cost() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
DECLARE
    bucket_ts TIMESTAMPTZ;
    key_id INT;
    limit_val INT;
BEGIN
    -- 计算 5min bucket（向下取整）
    bucket_ts := date_trunc('hour', NEW.ts)
                  + (FLOOR(EXTRACT(minute FROM NEW.ts) / 5) * INTERVAL '5 minutes');
    key_id := NEW.api_key_id;
    IF key_id IS NULL THEN
        RETURN NEW;
    END IF;

    -- 查找 api_key 的 rate_limit_rpm（作为该 key 的并发近似上限）
    -- 注意：api_keys 表没有 concurrency_limit 列（已在 realtime-trigger SQL 中确认）。
    -- 用 rate_limit_rpm / 10 作为近似（假设平均请求耗时 6 秒）。
    SELECT COALESCE(rate_limit_rpm, 0) / 10 INTO limit_val
    FROM api_keys WHERE id = key_id;

    -- 增量更新（注意：不在这里累加 active_concurrent，因为 AFTER INSERT 只能加不能减。
    -- active_concurrent 由 customer_cost_view 通过 JOIN request_logs 实时计算）
    INSERT INTO api_key_model_cost (
        bucket, api_key_id, canonical_id, raw_model, billing_mode,
        requests_total, requests_success,
        tokens_input, tokens_output, cost_usd,
        active_concurrent, concurrency_limit, pressure_ratio,
        last_request_at, updated_at
    ) VALUES (
        bucket_ts, key_id, NEW.canonical_id, COALESCE(NEW.outbound_model, NEW.client_model),
        'token',
        1, CASE WHEN NEW.success THEN 1 ELSE 0 END,
        COALESCE(NEW.prompt_tokens, 0), COALESCE(NEW.completion_tokens, 0),
        COALESCE(NEW.cost_usd, 0),
        1, limit_val,
        CASE WHEN limit_val > 0 THEN LEAST(1.0, 1.0 / limit_val) ELSE 0 END,
        NEW.ts, NOW()
    )
    ON CONFLICT (bucket, api_key_id, raw_model) DO UPDATE SET
        requests_total    = api_key_model_cost.requests_total + 1,
        requests_success  = api_key_model_cost.requests_success + CASE WHEN NEW.success THEN 1 ELSE 0 END,
        tokens_input      = api_key_model_cost.tokens_input + COALESCE(NEW.prompt_tokens, 0),
        tokens_output     = api_key_model_cost.tokens_output + COALESCE(NEW.completion_tokens, 0),
        cost_usd          = api_key_model_cost.cost_usd + COALESCE(NEW.cost_usd, 0),
        -- active_concurrent 在 trigger 中不更新（只在视图层动态计算）
        concurrency_limit = EXCLUDED.concurrency_limit,
        pressure_ratio    = CASE WHEN EXCLUDED.concurrency_limit > 0
                                  THEN LEAST(1.0, EXCLUDED.active_concurrent::numeric / EXCLUDED.concurrency_limit)
                                  ELSE 0 END,
        last_request_at   = NEW.ts,
        updated_at        = NOW();

    RETURN NEW;
END;
$$;


--
-- Name: update_provider_settings_updated_at(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.update_provider_settings_updated_at() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$;


SET default_table_access_method = heap;

--
-- Name: api_key_auto_profile; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_key_auto_profile (
    api_key_id integer NOT NULL,
    profile text DEFAULT 'smart'::text NOT NULL,
    first_chosen_at timestamp with time zone DEFAULT now(),
    last_used_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    CONSTRAINT api_key_auto_profile_profile_check CHECK ((profile = ANY (ARRAY['smart'::text, 'speed_first'::text, 'cost_first'::text])))
);


--
-- Name: TABLE api_key_auto_profile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.api_key_auto_profile IS 'Auto route: per-API-Key profile preference (sticky 30min)';


--
-- Name: api_key_model_cost; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_key_model_cost (
    bucket timestamp with time zone NOT NULL,
    api_key_id integer NOT NULL,
    canonical_id integer,
    raw_model text NOT NULL,
    billing_mode text,
    requests_total integer DEFAULT 0 NOT NULL,
    requests_success integer DEFAULT 0 NOT NULL,
    tokens_input bigint DEFAULT 0 NOT NULL,
    tokens_output bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(12,6) DEFAULT 0 NOT NULL,
    active_concurrent integer DEFAULT 0 NOT NULL,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    last_request_at timestamp with time zone,
    last_decision_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE api_key_model_cost; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.api_key_model_cost IS 'Auto route: per-API-Key per-model 5min rolled-up cost + concurrency + score';


--
-- Name: api_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.api_keys (
    id bigint NOT NULL,
    application_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    key_hash text NOT NULL,
    key_prefix text NOT NULL,
    owner_user text,
    data_sensitivity text DEFAULT 'internal'::text NOT NULL,
    default_end_user_id text,
    budget_usd numeric(14,6),
    rate_limit_rpm integer,
    enabled boolean DEFAULT true NOT NULL,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone,
    status character varying(16) DEFAULT 'active'::character varying NOT NULL,
    key_ciphertext text,
    is_system boolean DEFAULT false NOT NULL,
    rate_limit_concurrent integer,
    rate_limit_tpm integer,
    key_tier character varying(16) DEFAULT 'default'::character varying NOT NULL,
    key_ciphertext_kid text,
    throttled_at timestamp with time zone,
    throttled_reason text,
    ewma_rpm_baseline numeric(10,3),
    ewma_updated_at timestamp with time zone,
    reveal_count integer DEFAULT 0 NOT NULL,
    last_revealed_at timestamp with time zone,
    last_revealed_by text,
    remark text,
    key_alias text,
    total_requests bigint DEFAULT 0 NOT NULL,
    total_prompt_tokens bigint DEFAULT 0 NOT NULL,
    total_completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    total_cost_usd numeric(14,8) DEFAULT 0 NOT NULL,
    last_request_at timestamp with time zone,
    default_client_profile text,
    CONSTRAINT api_keys_data_sensitivity_check CHECK ((data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text]))),
    CONSTRAINT api_keys_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('pending'::character varying)::text, ('disabled'::character varying)::text, ('throttled'::character varying)::text, ('revoked'::character varying)::text])))
);


--
-- Name: COLUMN api_keys.status; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.status IS 'active | pending | disabled | throttled (auto-frozen) | revoked (permanent ban)';


--
-- Name: COLUMN api_keys.is_system; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.is_system IS 'System key - should not be disabled (e.g., admin login key)';


--
-- Name: COLUMN api_keys.rate_limit_concurrent; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.rate_limit_concurrent IS 'Per-key concurrent request cap (NULL = use tier default)';


--
-- Name: COLUMN api_keys.rate_limit_tpm; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.rate_limit_tpm IS 'Tokens per minute cap (NULL = no limit)';


--
-- Name: COLUMN api_keys.key_tier; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_tier IS 'system | production | default | applicant';


--
-- Name: COLUMN api_keys.key_ciphertext_kid; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_ciphertext_kid IS 'kid that was used when key_ciphertext was last written (v1 AES-GCM envelope)';


--
-- Name: COLUMN api_keys.throttled_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.throttled_at IS 'Timestamp when the key was auto-throttled by anomaly detection';


--
-- Name: COLUMN api_keys.ewma_rpm_baseline; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.ewma_rpm_baseline IS 'Rolling EWMA baseline RPM for anomaly detection (7-day window)';


--
-- Name: COLUMN api_keys.remark; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.remark IS 'Reason for key creation (system-created keys must explain why)';


--
-- Name: COLUMN api_keys.key_alias; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.key_alias IS 'Optional human-readable alias for the key';


--
-- Name: COLUMN api_keys.total_requests; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_requests IS 'Cumulative count of requests authenticated by this key';


--
-- Name: COLUMN api_keys.total_prompt_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_prompt_tokens IS 'Cumulative prompt token count';


--
-- Name: COLUMN api_keys.total_completion_tokens; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_completion_tokens IS 'Cumulative completion token count';


--
-- Name: COLUMN api_keys.total_cost_usd; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.total_cost_usd IS 'Cumulative cost in USD';


--
-- Name: COLUMN api_keys.last_request_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.api_keys.last_request_at IS 'When this key last made a request (denormalized from usage_ledger)';


--
-- Name: api_keys_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.api_keys_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: api_keys_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.api_keys_id_seq OWNED BY public.api_keys.id;


--
-- Name: applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.applications (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    owner_user text,
    data_sensitivity text DEFAULT 'internal'::text NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    default_client_profile text,
    allowed_models_json jsonb,
    CONSTRAINT applications_data_sensitivity_check CHECK ((data_sensitivity = ANY (ARRAY['public'::text, 'internal'::text, 'confidential'::text])))
);


--
-- Name: applications_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.applications_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: applications_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.applications_id_seq OWNED BY public.applications.id;


--
-- Name: auto_tune_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.auto_tune_audit (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    action text NOT NULL,
    old_limit integer,
    new_limit integer,
    reason text,
    peak_concurrent integer,
    p95_concurrent numeric(8,2),
    week_start timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    applied_by text
);


--
-- Name: TABLE auto_tune_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.auto_tune_audit IS 'Audit log for concurrency limit auto-tune actions (24h preview + auto-apply)';


--
-- Name: auto_tune_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.auto_tune_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: auto_tune_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.auto_tune_audit_id_seq OWNED BY public.auto_tune_audit.id;


--
-- Name: background_tasks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.background_tasks (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    task_type text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    status text DEFAULT 'running'::text NOT NULL,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    result_json jsonb,
    error text,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone
);


--
-- Name: background_tasks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.background_tasks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: background_tasks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.background_tasks_id_seq OWNED BY public.background_tasks.id;


--
-- Name: billing_orders; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.billing_orders (
    id bigint NOT NULL,
    order_no character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    order_type character varying(16) NOT NULL,
    status character varying(16) DEFAULT 'pending'::character varying NOT NULL,
    amount_cents integer NOT NULL,
    credits bigint NOT NULL,
    plan_id integer,
    package_id integer,
    payment_channel character varying(16) DEFAULT 'alipay'::character varying NOT NULL,
    qr_payload text DEFAULT ''::text NOT NULL,
    qr_url text DEFAULT ''::text NOT NULL,
    paid_at timestamp with time zone,
    expires_at timestamp with time zone NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT billing_orders_order_type_check CHECK (((order_type)::text = ANY (ARRAY[('subscribe'::character varying)::text, ('topup'::character varying)::text]))),
    CONSTRAINT billing_orders_payment_channel_check CHECK (((payment_channel)::text = ANY (ARRAY[('alipay'::character varying)::text, ('wechat'::character varying)::text, ('manual'::character varying)::text]))),
    CONSTRAINT billing_orders_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('paid'::character varying)::text, ('cancelled'::character varying)::text, ('expired'::character varying)::text])))
);


--
-- Name: billing_orders_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.billing_orders_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: billing_orders_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.billing_orders_id_seq OWNED BY public.billing_orders.id;


--
-- Name: candidate_failure_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.candidate_failure_logs (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id integer NOT NULL,
    provider_id integer NOT NULL,
    raw_model_name text NOT NULL,
    attempt_index integer DEFAULT 0 NOT NULL,
    error_kind text NOT NULL,
    error_message text,
    upstream_status_code integer,
    upstream_response_body text,
    upstream_response_preview text,
    latency_ms integer,
    retryable boolean,
    context jsonb
);


--
-- Name: TABLE candidate_failure_logs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.candidate_failure_logs IS 'Per-credential, per-model upstream failure log. Populated by routing/executor.go on every failed candidate attempt so transient diagnostics surface the actual vendor response (kind, status, body) instead of a generic "all N candidates failed" message. Used by candidate_failure_monitor for alerts and the admin candidate-failure API.';


--
-- Name: candidate_failure_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.candidate_failure_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: candidate_failure_logs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.candidate_failure_logs_id_seq OWNED BY public.candidate_failure_logs.id;


--
-- Name: credential_capabilities; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_capabilities (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    capability text NOT NULL,
    supported boolean DEFAULT false NOT NULL,
    last_tested_at timestamp with time zone,
    evidence_json jsonb,
    CONSTRAINT credential_capabilities_capability_check CHECK ((capability = ANY (ARRAY['tool_use'::text, 'vision'::text, 'streaming'::text, 'prompt_caching'::text, 'structured_output'::text, 'long_context'::text, 'json_mode'::text, 'batch'::text])))
);


--
-- Name: credential_capabilities_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_capabilities_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_capabilities_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_capabilities_id_seq OWNED BY public.credential_capabilities.id;


--
-- Name: credential_health_checks; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_health_checks (
    id bigint NOT NULL,
    run_id bigint,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    provider_id bigint NOT NULL,
    credential_id bigint NOT NULL,
    models_ok boolean DEFAULT false NOT NULL,
    probe_ok boolean DEFAULT false NOT NULL,
    health_status text NOT NULL,
    warning_code text,
    classification_reason text,
    models_failure_reason text,
    models_http_status integer,
    probe_http_status integer,
    models_latency_ms integer,
    probe_latency_ms integer,
    probe_model text,
    models_error text,
    probe_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_credential_health_checks_models_failure_reason CHECK (((models_failure_reason IS NULL) OR (models_failure_reason = ANY (ARRAY['request_failed'::text, 'empty_models'::text, 'invalid_payload'::text, 'not_supported'::text])))),
    CONSTRAINT chk_credential_health_checks_status CHECK ((health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'warning'::text, 'unreachable'::text])))
);


--
-- Name: credential_health_checks_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_health_checks_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_health_checks_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_health_checks_id_seq OWNED BY public.credential_health_checks.id;


--
-- Name: credential_model_bindings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_bindings (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    provider_model_id bigint NOT NULL,
    routing_tier smallint DEFAULT 2,
    weight smallint DEFAULT 100,
    manual_priority smallint DEFAULT 99,
    success_rate numeric,
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    consecutive_failures integer DEFAULT 0,
    unit_price_in_per_1m numeric,
    unit_price_out_per_1m numeric,
    cache_read_price_per_1m numeric,
    cache_write_price_per_1m numeric,
    currency text DEFAULT 'USD'::text,
    billing_mode text DEFAULT 'per_token'::text,
    pricing_source text,
    pricing_updated_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    available boolean DEFAULT true NOT NULL,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    plan_meta jsonb DEFAULT '{}'::jsonb NOT NULL,
    admin_protected boolean DEFAULT false NOT NULL,
    unavailable_recover_at timestamp with time zone
);


--
-- Name: TABLE credential_model_bindings; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_bindings IS 'Many-to-many: which credential can access which model, with routing/pricing attrs';


--
-- Name: COLUMN credential_model_bindings.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_bindings.billing_mode IS 'Billing mode: token (PAYG per-1M) | token_plan (prepaid credits/package) | code_plan (subscription, monthly fee + bundle) | free (rate=0) | per_token/per_request/monthly (legacy aliases)';


--
-- Name: COLUMN credential_model_bindings.plan_meta; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credential_model_bindings.plan_meta IS 'Subscription/plan metadata: {monthly_cny, included_tokens, tier, validity_days, modality, etc.}. Mirrors pricing_plans.plan_json at offer level.';


--
-- Name: credential_model_bindings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_model_bindings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_model_bindings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_model_bindings_id_seq OWNED BY public.credential_model_bindings.id;


--
-- Name: credential_model_call_history; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_call_history (
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    window_start timestamp with time zone NOT NULL,
    total_calls integer DEFAULT 0 NOT NULL,
    success_calls integer DEFAULT 0 NOT NULL,
    failed_calls integer DEFAULT 0 NOT NULL,
    avg_latency_ms numeric(8,2),
    p95_latency_ms integer,
    p99_latency_ms integer,
    error_rate_limit_count integer DEFAULT 0 NOT NULL,
    error_quota_count integer DEFAULT 0 NOT NULL,
    error_concurrent_count integer DEFAULT 0 NOT NULL,
    error_network_count integer DEFAULT 0 NOT NULL,
    error_auth_count integer DEFAULT 0 NOT NULL,
    error_other_count integer DEFAULT 0 NOT NULL,
    avg_concurrent numeric(5,2),
    peak_concurrent integer,
    created_at timestamp with time zone DEFAULT now()
);


--
-- Name: credential_model_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_index (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text NOT NULL,
    canonical_id integer,
    billing_mode text,
    unit_price_in_per_1m numeric(10,4),
    unit_price_out_per_1m numeric(10,4),
    context_window integer,
    success_rate numeric(5,4),
    p95_latency_ms integer,
    active_sessions integer DEFAULT 0,
    concurrency_limit integer,
    pressure_ratio numeric(5,4),
    score_smart numeric(8,4),
    score_speed_first numeric(8,4),
    score_cost_first numeric(8,4),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE credential_model_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_index IS 'Auto route: per-credential 5min rolled-up live score with 3 profile precomputed';


--
-- Name: credential_model_peak_1m; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_peak_1m (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    peak_concurrent integer DEFAULT 0 NOT NULL,
    avg_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL
);


--
-- Name: TABLE credential_model_peak_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_peak_1m IS 'Per-minute peak concurrency per credential-model pair (used by auto-tune)';


--
-- Name: credential_model_stats_1m; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_stats_1m (
    bucket timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model text DEFAULT ''::text NOT NULL,
    requests integer DEFAULT 0 NOT NULL,
    successes integer DEFAULT 0 NOT NULL,
    failures integer DEFAULT 0 NOT NULL,
    latency_p50_ms integer,
    latency_p95_ms integer,
    latency_p99_ms integer,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(14,8) DEFAULT 0 NOT NULL,
    error_counts jsonb DEFAULT '{}'::jsonb NOT NULL
);


--
-- Name: TABLE credential_model_stats_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_stats_1m IS 'Per-minute aggregated routing stats, used for sliding window queries';


--
-- Name: credential_model_weekly_peak; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_model_weekly_peak (
    week_start timestamp with time zone NOT NULL,
    credential_id bigint NOT NULL,
    raw_model text DEFAULT ''::text NOT NULL,
    peak_concurrent integer DEFAULT 0 NOT NULL,
    peak_concurrent_5min integer DEFAULT 0 NOT NULL,
    p95_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    avg_concurrent numeric(8,2) DEFAULT 0 NOT NULL,
    total_requests bigint DEFAULT 0 NOT NULL,
    sample_days integer DEFAULT 0 NOT NULL,
    current_limit integer DEFAULT 0 NOT NULL,
    suggested_limit integer,
    suggestion_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE credential_model_weekly_peak; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.credential_model_weekly_peak IS 'Weekly aggregated peak concurrency for auto-tune suggestions';


--
-- Name: credential_probe_model_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_probe_model_log (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id bigint NOT NULL,
    source text NOT NULL,
    old_model text,
    new_model text,
    actor text,
    reason text,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: credential_probe_model_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_probe_model_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_probe_model_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_probe_model_log_id_seq OWNED BY public.credential_probe_model_log.id;


--
-- Name: credential_quota_usage; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quota_usage (
    id bigint NOT NULL,
    quota_id bigint NOT NULL,
    window_started_at timestamp with time zone NOT NULL,
    window_ends_at timestamp with time zone NOT NULL,
    used_total_tokens bigint DEFAULT 0 NOT NULL,
    used_input_tokens bigint DEFAULT 0 NOT NULL,
    used_output_tokens bigint DEFAULT 0 NOT NULL,
    used_requests bigint DEFAULT 0 NOT NULL,
    used_cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    last_event_at timestamp with time zone,
    exhausted boolean DEFAULT false NOT NULL
);


--
-- Name: credential_quota_usage_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_quota_usage_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_quota_usage_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_quota_usage_id_seq OWNED BY public.credential_quota_usage.id;


--
-- Name: credential_quotas; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credential_quotas (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    quota_name text NOT NULL,
    window_type text NOT NULL,
    starts_at timestamp with time zone,
    ends_at timestamp with time zone,
    period text,
    cron_expr text,
    timezone text DEFAULT 'UTC'::text NOT NULL,
    reset_anchor_local time without time zone,
    rolling_seconds integer,
    cap_total_tokens bigint,
    cap_input_tokens bigint,
    cap_output_tokens bigint,
    cap_requests bigint,
    cap_cost_usd numeric(14,6),
    unlimited_in_window boolean DEFAULT false NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    priority integer DEFAULT 100 NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT credential_quotas_window_type_check CHECK ((window_type = ANY (ARRAY['fixed'::text, 'recurring'::text, 'rolling'::text])))
);


--
-- Name: credential_quotas_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credential_quotas_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credential_quotas_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credential_quotas_id_seq OWNED BY public.credential_quotas.id;


--
-- Name: credentials; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credentials (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    label text NOT NULL,
    secret_ciphertext bytea,
    secret_kid text,
    trust_level text DEFAULT 'trusted'::text NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    concurrency_limit integer,
    effective_concurrency integer,
    balance_usd numeric(14,6),
    pricing_distrust boolean DEFAULT false NOT NULL,
    relay_overhead_ms integer,
    active_plan_id bigint,
    plan_consumed_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    api_models_ok boolean,
    api_models_last_checked_at timestamp with time zone,
    api_models_error text,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    circuit_state text DEFAULT 'closed'::text,
    circuit_opened_at timestamp with time zone,
    consecutive_failures integer DEFAULT 0,
    cooling_until timestamp with time zone,
    circuit_open_count_window integer DEFAULT 0,
    circuit_window_started_at timestamp with time zone,
    effective_at timestamp with time zone,
    expires_at timestamp with time zone,
    tags jsonb DEFAULT '[]'::jsonb,
    notes text,
    health_status text DEFAULT 'unknown'::text NOT NULL,
    health_checked_at timestamp with time zone,
    health_source text,
    health_warning_code text,
    health_error text,
    health_latency_ms integer,
    health_probe_model text,
    lifecycle_status text DEFAULT 'active'::text NOT NULL,
    availability_state text DEFAULT 'ready'::text NOT NULL,
    quota_state text DEFAULT 'ok'::text NOT NULL,
    state_reason_code text,
    state_reason_detail text,
    state_updated_at timestamp with time zone,
    availability_recover_at timestamp with time zone,
    quota_recover_at timestamp with time zone,
    balance_currency text DEFAULT 'USD'::text,
    balance_last_checked_at timestamp with time zone,
    balance_check_endpoint text,
    pool_group text,
    acquisition_source text,
    acquisition_detail text,
    manual_disabled boolean DEFAULT false NOT NULL,
    default_probe_model text,
    default_probe_model_source text,
    default_probe_model_picked_at timestamp with time zone,
    concurrency_limit_auto integer,
    fp_slot_limit integer NOT NULL,
    CONSTRAINT chk_credentials_health_source CHECK (((health_source IS NULL) OR (health_source = ANY (ARRAY['models'::text, 'probe'::text, 'mixed'::text, 'none'::text, 'fast_reprobe'::text])))),
    CONSTRAINT chk_credentials_health_status CHECK ((health_status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'warning'::text, 'unreachable'::text]))),
    CONSTRAINT credentials_availability_state_check CHECK ((availability_state = ANY (ARRAY['ready'::text, 'cooling'::text, 'rate_limited'::text, 'auth_failed'::text, 'unreachable'::text, 'suspended'::text]))),
    CONSTRAINT credentials_circuit_state_chk CHECK ((circuit_state = ANY (ARRAY['closed'::text, 'open'::text, 'half_open'::text]))),
    CONSTRAINT credentials_fp_slot_limit_check CHECK (((fp_slot_limit >= 0) AND (fp_slot_limit <= 10000))),
    CONSTRAINT credentials_fp_slot_vs_concurrency CHECK (((concurrency_limit IS NULL) OR (fp_slot_limit IS NULL) OR (fp_slot_limit <= concurrency_limit))),
    CONSTRAINT credentials_lifecycle_status_check CHECK ((lifecycle_status = ANY (ARRAY['active'::text, 'disabled'::text, 'suspended'::text, 'retired'::text]))),
    CONSTRAINT credentials_status_check CHECK ((status = ANY (ARRAY['active'::text, 'cooling'::text, 'degraded'::text, 'quarantine'::text, 'quota_expired'::text, 'disabled'::text]))),
    CONSTRAINT credentials_trust_level_check CHECK ((trust_level = ANY (ARRAY['trusted'::text, 'cooling'::text, 'degraded'::text, 'quarantine'::text])))
);


--
-- Name: COLUMN credentials.api_models_ok; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_ok IS '最近一次模型清单 API 拉取是否成功（NULL=未验证）';


--
-- Name: COLUMN credentials.api_models_last_checked_at; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_last_checked_at IS '最近一次模型清单 API 验证时间';


--
-- Name: COLUMN credentials.api_models_error; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.api_models_error IS '最近一次模型清单 API 验证失败原因（HTTP 状态码 + 错误摘要，已脱敏）';


--
-- Name: COLUMN credentials.balance_check_endpoint; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.balance_check_endpoint IS 'URL template to check remaining balance';


--
-- Name: COLUMN credentials.pool_group; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.pool_group IS 'free | shared | dedicated | NULL';


--
-- Name: COLUMN credentials.acquisition_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.acquisition_source IS 'Free pool: signup | env | oauth | mirrored | discovered | no_key | manual';


--
-- Name: COLUMN credentials.acquisition_detail; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.acquisition_detail IS 'Free pool source detail: env var name, mirror source label, oauth file, signup URL, etc.';


--
-- Name: COLUMN credentials.fp_slot_limit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.credentials.fp_slot_limit IS 'Fingerprint slot pool size: number of distinct virtual user identities this credential can simulate. 0 = unlimited. Distinct from concurrency_limit which controls in-flight request count.';


--
-- Name: credentials_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credentials_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credentials_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credentials_id_seq OWNED BY public.credentials.id;


--
-- Name: credit_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.credit_ledger (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    entry_type character varying(32) NOT NULL,
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    ref_type character varying(32),
    ref_id character varying(128),
    note text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    pool character varying(32),
    CONSTRAINT credit_ledger_entry_type_check CHECK (((entry_type)::text = ANY (ARRAY[('consume'::character varying)::text, ('topup'::character varying)::text, ('subscribe'::character varying)::text, ('adjust'::character varying)::text, ('refund'::character varying)::text])))
);


--
-- Name: credit_ledger_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.credit_ledger_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: credit_ledger_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.credit_ledger_id_seq OWNED BY public.credit_ledger.id;


--
-- Name: request_logs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.request_logs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: request_logs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
)
PARTITION BY RANGE (ts);

ALTER TABLE ONLY public.request_logs FORCE ROW LEVEL SECURITY;


--
-- Name: COLUMN request_logs.cost_display; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.cost_display IS 'Request-level displayed cost in its native currency; may differ from cost_usd when provider pricing is not USD.';


--
-- Name: COLUMN request_logs.cost_currency; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.cost_currency IS 'Currency for request_logs.cost_display, e.g. USD/CNY.';


--
-- Name: COLUMN request_logs.is_auto_request; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.is_auto_request IS 'Auto route: was this request model=auto?';


--
-- Name: COLUMN request_logs.task_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.task_type IS 'Auto route: classified task type (chat/reasoning/code/...)';


--
-- Name: COLUMN request_logs.auto_profile; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_profile IS 'Auto route: profile used (smart/speed_first/cost_first)';


--
-- Name: COLUMN request_logs.auto_decision; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_decision IS 'Auto route: top-N candidates + chosen model + scoring breakdown';


--
-- Name: COLUMN request_logs.auto_confidence; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.auto_confidence IS 'Auto route: classification confidence 0-1';


--
-- Name: COLUMN request_logs.parent_request_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.parent_request_id IS 'Round 47 (2026-06-18): the pre-compression request_id when compressor rewrote the body. NULL for uncompressed rows. Single-level chain only (child has at most 1 parent).';


--
-- Name: COLUMN request_logs.compression_reason; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_reason IS 'Round 47 (2026-06-18): why compression fired. mode_1_auto_threshold = body > cand.ContextWindow × 0.8 × 3.5 (LLM_GATEWAY_COMPRESSION_MODE=1). mode_2_on_4xx = upstream 4xx context_length_exceeded (LLM_GATEWAY_COMPRESSION_MODE=2). NULL = no compression.';


--
-- Name: COLUMN request_logs.compression_strategy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_strategy IS 'Round 47 (2026-06-18): which decompression path succeeded. mechanical_trim = oldest-pair drop (transform/ctx_compress.go). memora_l1_inject = dynamic_context user message from Memora /product/search. llm_summary = 1M-context model summary. noop = attempted but skipped (e.g. warmup_min_facts guard).';


--
-- Name: COLUMN request_logs.compression_meta; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.compression_meta IS 'Round 47 (2026-06-18): compression telemetry. Schema: {tokens_before, tokens_after, bytes_before, bytes_after, context_window_used, threshold_bytes, dropped_messages, summary_chars, model_used, latency_ms, memora_facts_used, warmup_skipped, first_user_retained, system_retained, reason_detail}. See v7 §3.2.';


--
-- Name: COLUMN request_logs.upstream_finish_reason; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.request_logs.upstream_finish_reason IS '2026-06-19 T-NEW-7: the SOLE home for the upstream finish_reason
     (stop, tool_calls, length, end_turn, function_call, max_tokens, …).
     NULL means the stream ended without a finish_reason (e.g. truncated
     pre-finish).  Populated for BOTH success and failure rows.
     This column REPLACES the prior use of failure_detail_code for
     finish reasons; see the migration header for the full rationale.';


--
-- Name: customer_cost_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.customer_cost_view AS
 SELECT akmc.api_key_id,
    ak.key_alias,
    ak.tenant_id,
    ak.application_id,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_1h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '24:00:00'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_24h,
    sum(
        CASE
            WHEN (akmc.bucket >= (now() - '7 days'::interval)) THEN akmc.cost_usd
            ELSE (0)::numeric
        END) AS cost_usd_7d,
    sum(akmc.requests_total) AS total_auto_requests,
    sum(akmc.requests_success) AS total_auto_success,
    ( SELECT count(*) AS count
           FROM public.request_logs rl
          WHERE ((rl.api_key_id = akmc.api_key_id) AND (rl.is_auto_request = true) AND (rl.ts >= (now() - '00:05:00'::interval)) AND (rl.success IS NOT NULL) AND (rl.ts IS NOT NULL))) AS active_concurrent,
    max(akmc.concurrency_limit) AS concurrency_limit,
    avg(
        CASE
            WHEN (akmc.bucket >= (now() - '01:00:00'::interval)) THEN akmc.pressure_ratio
            ELSE NULL::numeric
        END) AS avg_pressure_1h,
    max(akmc.score_smart) AS best_score_smart,
    max(akmc.score_speed_first) AS best_score_speed_first,
    max(akmc.score_cost_first) AS best_score_cost_first,
    max(akmc.last_request_at) AS last_request_at
   FROM (public.api_key_model_cost akmc
     JOIN public.api_keys ak ON ((ak.id = akmc.api_key_id)))
  GROUP BY akmc.api_key_id, ak.key_alias, ak.tenant_id, ak.application_id;


--
-- Name: VIEW customer_cost_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.customer_cost_view IS 'Auto route: per-API-Key customer cost dashboard (1h/24h/7d windows + concurrency + scores). active_concurrent is computed live from request_logs (5min window).';


--
-- Name: internal_service_keys; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.internal_service_keys (
    service_id text NOT NULL,
    secret_hash text NOT NULL,
    description text,
    enabled boolean DEFAULT true NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    rotated_at timestamp with time zone,
    rotation_notes text
);


--
-- Name: TABLE internal_service_keys; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.internal_service_keys IS 'Registry of HMAC secrets for internal service-to-service authentication.
     The actual secret is stored in INTERNAL_SERVICE_KEYS_JSON env var (not here).
     This table tracks registration metadata and last-used timestamps for audit.';


--
-- Name: key_applications; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.key_applications (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    client_ip inet NOT NULL,
    fingerprint text NOT NULL,
    contact text NOT NULL,
    purpose text,
    status text DEFAULT 'pending'::text NOT NULL,
    issued_key_id bigint,
    admin_notes text,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    expires_at timestamp with time zone DEFAULT (now() + '24:00:00'::interval) NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT key_applications_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'expired'::text])))
);


--
-- Name: key_rpm_daily; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.key_rpm_daily (
    api_key_id bigint NOT NULL,
    day_bucket date NOT NULL,
    peak_rpm numeric(10,3) DEFAULT 0 NOT NULL,
    avg_rpm numeric(10,3) DEFAULT 0 NOT NULL,
    request_count bigint DEFAULT 0 NOT NULL
);


--
-- Name: local_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_models (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    canonical_id bigint,
    raw_name text NOT NULL,
    quantization text,
    size_bytes bigint,
    family text,
    parameters_b numeric(8,2),
    loaded boolean DEFAULT false NOT NULL,
    keep_alive_seconds integer DEFAULT 0 NOT NULL,
    last_used_at timestamp with time zone
);


--
-- Name: local_models_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.local_models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: local_models_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.local_models_id_seq OWNED BY public.local_models.id;


--
-- Name: local_runtimes; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.local_runtimes (
    id bigint NOT NULL,
    host_code text NOT NULL,
    runtime_type text NOT NULL,
    base_url text NOT NULL,
    mode text DEFAULT 'direct'::text NOT NULL,
    status text DEFAULT 'unknown'::text NOT NULL,
    gpu_info_json jsonb,
    vram_total_mb integer,
    vram_used_mb integer,
    ram_total_mb integer,
    last_heartbeat_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT local_runtimes_mode_check CHECK ((mode = ANY (ARRAY['direct'::text, 'agent'::text]))),
    CONSTRAINT local_runtimes_runtime_type_check CHECK ((runtime_type = ANY (ARRAY['ollama'::text, 'vllm'::text, 'llamacpp'::text, 'lmstudio'::text, 'mlx'::text]))),
    CONSTRAINT local_runtimes_status_check CHECK ((status = ANY (ARRAY['unknown'::text, 'healthy'::text, 'degraded'::text, 'offline'::text])))
);


--
-- Name: local_runtimes_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.local_runtimes_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: local_runtimes_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.local_runtimes_id_seq OWNED BY public.local_runtimes.id;


--
-- Name: maas_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.maas_settings (
    id integer DEFAULT 1 NOT NULL,
    cents_per_credit numeric(10,4) DEFAULT 0.1 NOT NULL,
    base_credits_per_1m bigint DEFAULT 10000 NOT NULL,
    currency_display character varying(8) DEFAULT 'CNY'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    alipay_account character varying(128) DEFAULT ''::character varying NOT NULL,
    wechat_mch_id character varying(128) DEFAULT ''::character varying NOT NULL,
    stub_alipay_qr_url text DEFAULT ''::text NOT NULL,
    stub_wechat_qr_url text DEFAULT ''::text NOT NULL,
    base_credits_per_1m_out bigint,
    base_credits_per_1m_cache_in bigint,
    base_credits_per_1m_cache_out bigint,
    global_discount numeric(6,4) DEFAULT 1.0 NOT NULL,
    CONSTRAINT maas_settings_id_check CHECK ((id = 1))
);


--
-- Name: model_aliases; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_aliases (
    id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    raw_name text NOT NULL,
    quantization text,
    surface text,
    status text DEFAULT 'active'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    client_profiles text[],
    CONSTRAINT model_aliases_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: model_aliases_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_aliases_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_aliases_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_aliases_id_seq OWNED BY public.model_aliases.id;


--
-- Name: model_cost_per_task_view; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_cost_per_task_view AS
 SELECT mcp.canonical_id,
    mcp.raw_model,
    sum(mcp.cost_usd) AS total_cost_usd,
    sum((mcp.tokens_input + mcp.tokens_output)) AS total_tokens,
        CASE
            WHEN (sum((mcp.tokens_input + mcp.tokens_output)) > (0)::numeric) THEN ((sum(mcp.cost_usd) / sum((mcp.tokens_input + mcp.tokens_output))) * (1000000)::numeric)
            ELSE (0)::numeric
        END AS avg_cost_per_1m_usd,
        CASE
            WHEN (sum(mcp.requests_total) > 0) THEN ((sum(mcp.requests_success))::numeric / (sum(mcp.requests_total))::numeric)
            ELSE (0)::numeric
        END AS success_rate,
    ( SELECT avg(rl.latency_ms) AS avg
           FROM public.request_logs rl
          WHERE ((rl.outbound_model = mcp.raw_model) AND (rl.success = true) AND (rl.ts >= (now() - '7 days'::interval)))) AS avg_latency_ms,
    sum(mcp.requests_total) AS total_requests,
    count(DISTINCT mcp.api_key_id) AS unique_api_keys
   FROM public.api_key_model_cost mcp
  WHERE (mcp.bucket >= (now() - '7 days'::interval))
  GROUP BY mcp.canonical_id, mcp.raw_model;


--
-- Name: VIEW model_cost_per_task_view; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.model_cost_per_task_view IS 'Auto route: per-model aggregated cost for last 7 days';


--
-- Name: model_credit_rates; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_credit_rates (
    canonical_id integer NOT NULL,
    credits_per_1m_in bigint,
    credits_per_1m_out bigint,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    credits_per_1m_cache_in bigint,
    credits_per_1m_cache_out bigint,
    manual_in boolean DEFAULT false NOT NULL,
    manual_out boolean DEFAULT false NOT NULL,
    manual_cache_in boolean DEFAULT false NOT NULL,
    manual_cache_out boolean DEFAULT false NOT NULL
);


--
-- Name: model_discovery_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_discovery_runs (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    trigger text DEFAULT 'manual'::text NOT NULL,
    status text DEFAULT 'running'::text NOT NULL,
    started_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    heartbeat_at timestamp with time zone DEFAULT now() NOT NULL,
    lease_expires_at timestamp with time zone NOT NULL,
    requested_by text,
    request_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    summary_json jsonb,
    error text,
    CONSTRAINT chk_model_discovery_runs_status CHECK ((status = ANY (ARRAY['running'::text, 'succeeded'::text, 'failed'::text]))),
    CONSTRAINT chk_model_discovery_runs_trigger CHECK ((trigger = ANY (ARRAY['manual'::text, 'scheduled'::text, 'credential_added'::text])))
);


--
-- Name: model_discovery_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_discovery_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_discovery_runs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_discovery_runs_id_seq OWNED BY public.model_discovery_runs.id;


--
-- Name: model_families; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_families (
    id text NOT NULL,
    display_name text NOT NULL,
    vendor text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_families_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: model_fingerprints; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_fingerprints (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint NOT NULL,
    fingerprint_hash text NOT NULL,
    sampled_features_json jsonb,
    last_verified_at timestamp with time zone,
    drift_detected boolean DEFAULT false NOT NULL
);


--
-- Name: model_fingerprints_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_fingerprints_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_fingerprints_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_fingerprints_id_seq OWNED BY public.model_fingerprints.id;


--
-- Name: model_lifecycle_jobs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_lifecycle_jobs (
    id bigint NOT NULL,
    runtime_id bigint NOT NULL,
    action text NOT NULL,
    target text NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    progress_pct numeric(5,2) DEFAULT 0,
    log text,
    started_at timestamp with time zone,
    finished_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT model_lifecycle_jobs_action_check CHECK ((action = ANY (ARRAY['pull'::text, 'rm'::text, 'load'::text, 'unload'::text, 'keepalive'::text]))),
    CONSTRAINT model_lifecycle_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'success'::text, 'failed'::text, 'canceled'::text])))
);


--
-- Name: model_lifecycle_jobs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_lifecycle_jobs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_lifecycle_jobs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_lifecycle_jobs_id_seq OWNED BY public.model_lifecycle_jobs.id;


--
-- Name: model_offer_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offer_events (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    source text NOT NULL,
    action text NOT NULL,
    credential_id bigint NOT NULL,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    reason_code text,
    reason_detail text,
    request_id text,
    run_id bigint,
    metadata_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    CONSTRAINT model_offer_events_action_check CHECK ((action = ANY (ARRAY['disable'::text, 'enable'::text]))),
    CONSTRAINT model_offer_events_source_check CHECK ((source = ANY (ARRAY['runtime'::text, 'discovery'::text, 'admin'::text, 'migration'::text, 'manual'::text])))
);


--
-- Name: model_offer_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_offer_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_offer_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_offer_events_id_seq OWNED BY public.model_offer_events.id;


--
-- Name: provider_models; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_models (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    raw_model_name text NOT NULL,
    canonical_id bigint,
    standardized_name text,
    outbound_model_name text,
    available boolean DEFAULT true NOT NULL,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE provider_models; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.provider_models IS 'Provider-exposed models: one row per (provider, raw_model_name)';


--
-- Name: COLUMN provider_models.canonical_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_models.canonical_id IS 'FK to models_canonical.id for canonical name resolution';


--
-- Name: model_offers; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.model_offers AS
 SELECT cmb.id,
    cmb.credential_id,
    pm.canonical_id,
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
    cmb.billing_mode,
    cmb.pricing_source,
    cmb.pricing_updated_at,
    cmb.manual_priority,
    cmb.active_sessions,
    cmb.consecutive_failures,
    cmb.admin_protected
   FROM (public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));


--
-- Name: COLUMN model_offers.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers.billing_mode IS 'Billing mode: token (PAYG per-1M) | token_plan (prepaid credits/package) | code_plan (subscription, monthly fee + bundle) | free (rate=0) | per_token/per_request/monthly (legacy aliases)';


--
-- Name: model_offers_legacy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_offers_legacy (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean DEFAULT true NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    routing_tier smallint DEFAULT 2,
    weight smallint DEFAULT 100,
    unit_price_in_per_1m numeric(12,6),
    unit_price_out_per_1m numeric(12,6),
    currency text DEFAULT 'USD'::text,
    outbound_model_name text,
    cache_read_price_per_1m numeric(12,6),
    cache_write_price_per_1m numeric(12,6),
    standardized_name text,
    unavailable_reason text,
    unavailable_at timestamp with time zone,
    billing_mode text DEFAULT 'per_token'::text,
    pricing_source text,
    pricing_updated_at timestamp with time zone,
    manual_priority smallint DEFAULT 99,
    active_sessions integer DEFAULT 0,
    consecutive_failures integer DEFAULT 0,
    CONSTRAINT model_offers_manual_priority_chk CHECK (((manual_priority >= 1) AND (manual_priority <= 99))),
    CONSTRAINT model_offers_routing_tier_chk CHECK (((routing_tier >= 1) AND (routing_tier <= 9))),
    CONSTRAINT model_offers_weight_chk CHECK (((weight >= 1) AND (weight <= 1000)))
);


--
-- Name: COLUMN model_offers_legacy.cache_read_price_per_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.cache_read_price_per_1m IS 'Per-million-token price for cache reads (NULL = use unit_price_in_per_1m)';


--
-- Name: COLUMN model_offers_legacy.cache_write_price_per_1m; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.cache_write_price_per_1m IS 'Per-million-token price for cache writes (NULL = use unit_price_in_per_1m)';


--
-- Name: COLUMN model_offers_legacy.standardized_name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.standardized_name IS 'Standardized model name in format: family-version[-feature], e.g. "minimax-m2.7", "glm-4.5-flash", "claude-opus-4.8". Auto-filled on discovery, can be manually edited.';


--
-- Name: COLUMN model_offers_legacy.billing_mode; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.billing_mode IS 'per_token | per_request | monthly | free';


--
-- Name: COLUMN model_offers_legacy.pricing_source; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.model_offers_legacy.pricing_source IS 'manual | scraped | inherited';


--
-- Name: model_offers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_offers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_offers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_offers_id_seq OWNED BY public.model_offers_legacy.id;


--
-- Name: model_probe_runs; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_runs (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    status text NOT NULL,
    http_status integer,
    error_code text,
    error_message text,
    latency_ms integer DEFAULT 0 NOT NULL,
    state_change text,
    state_applied boolean DEFAULT true NOT NULL,
    triggered_by text DEFAULT 'scheduler'::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: model_probe_runs_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_probe_runs_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_probe_runs_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_probe_runs_id_seq OWNED BY public.model_probe_runs.id;


--
-- Name: model_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_probe_state (
    credential_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    state text DEFAULT 'unknown'::text NOT NULL,
    consecutive_successes integer DEFAULT 0 NOT NULL,
    consecutive_failures integer DEFAULT 0 NOT NULL,
    total_attempts integer DEFAULT 0 NOT NULL,
    last_attempt_at timestamp with time zone,
    next_retry_at timestamp with time zone DEFAULT now() NOT NULL,
    last_status text,
    last_state_change_at timestamp with time zone,
    last_state_change_run bigint,
    last_unavailable_reason text,
    last_err_code text,
    next_retry_at_override timestamp with time zone
);


--
-- Name: model_reconcile_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_reconcile_log (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    credential_id bigint,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    added integer DEFAULT 0 NOT NULL,
    removed integer DEFAULT 0 NOT NULL,
    changed integer DEFAULT 0 NOT NULL,
    diff_json jsonb
);


--
-- Name: model_reconcile_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.model_reconcile_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: model_reconcile_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.model_reconcile_log_id_seq OWNED BY public.model_reconcile_log.id;


--
-- Name: model_task_index; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.model_task_index (
    bucket timestamp with time zone NOT NULL,
    canonical_id integer NOT NULL,
    task_type text NOT NULL,
    sample_count integer DEFAULT 0 NOT NULL,
    success_rate numeric(5,4),
    avg_latency_ms integer,
    p95_latency_ms integer,
    avg_cost_per_1k_usd numeric(10,6),
    primary_credential_id bigint,
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE model_task_index; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.model_task_index IS 'Auto route: per-model-per-task 5min rolled-up performance (success/latency/cost)';


--
-- Name: models_canonical; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.models_canonical (
    id bigint NOT NULL,
    canonical_name text NOT NULL,
    family text,
    parameters_b numeric(8,2),
    modality text DEFAULT 'text'::text NOT NULL,
    context_window integer,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    tags_locked boolean DEFAULT false NOT NULL,
    tags_updated_at timestamp with time zone,
    display_name text,
    status text DEFAULT 'active'::text NOT NULL,
    source text DEFAULT 'db'::text NOT NULL,
    disabled_reason text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    input_price_cny numeric(10,4) DEFAULT 0,
    output_price_cny numeric(10,4) DEFAULT 0,
    CONSTRAINT models_canonical_modality_check CHECK ((modality = ANY (ARRAY['text'::text, 'vision'::text, 'audio'::text, 'multimodal'::text, 'embedding'::text]))),
    CONSTRAINT models_canonical_status_check CHECK ((status = ANY (ARRAY['active'::text, 'disabled'::text, 'deprecated'::text, 'hidden'::text])))
);


--
-- Name: COLUMN models_canonical.input_price_cny; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.input_price_cny IS 'Input price in CNY per million tokens (0 = not set/unknown)';


--
-- Name: COLUMN models_canonical.output_price_cny; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.models_canonical.output_price_cny IS 'Output price in CNY per million tokens (0 = not set/unknown)';


--
-- Name: models_canonical_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.models_canonical_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: models_canonical_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.models_canonical_id_seq OWNED BY public.models_canonical.id;


--
-- Name: ops_model_offers_backup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.ops_model_offers_backup (
    backup_id bigint NOT NULL,
    run_tag text NOT NULL,
    backed_at timestamp with time zone DEFAULT now() NOT NULL,
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    raw_model_name text NOT NULL,
    p95_latency_ms integer,
    success_rate numeric(5,4),
    available boolean NOT NULL,
    last_seen_at timestamp with time zone NOT NULL
);


--
-- Name: ops_model_offers_backup_backup_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.ops_model_offers_backup_backup_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: ops_model_offers_backup_backup_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.ops_model_offers_backup_backup_id_seq OWNED BY public.ops_model_offers_backup.backup_id;


--
-- Name: passive_probe_state; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.passive_probe_state (
    credential_id integer NOT NULL,
    raw_model_name text NOT NULL,
    error_kind text NOT NULL,
    consecutive_count integer DEFAULT 0 NOT NULL,
    total_recent_count integer DEFAULT 0 NOT NULL,
    window_total_count integer DEFAULT 0 NOT NULL,
    first_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    in_reviewing boolean DEFAULT false NOT NULL,
    reviewing_until timestamp with time zone,
    final_marked_at timestamp with time zone,
    unavailable_reason text,
    last_response_body_preview text
);


--
-- Name: price_change_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.price_change_events (
    id bigint NOT NULL,
    old_plan_id bigint,
    new_plan_id bigint NOT NULL,
    delta_json jsonb,
    detected_at timestamp with time zone DEFAULT now() NOT NULL,
    notify_channel text,
    applied boolean DEFAULT false NOT NULL
);


--
-- Name: price_change_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.price_change_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: price_change_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.price_change_events_id_seq OWNED BY public.price_change_events.id;


--
-- Name: pricing_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_plans (
    id bigint NOT NULL,
    scope text NOT NULL,
    provider_id bigint,
    credential_id bigint,
    tenant_id text,
    model_canonical_id bigint,
    plan_type text NOT NULL,
    currency text DEFAULT 'USD'::text NOT NULL,
    plan_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    effective_from timestamp with time zone DEFAULT now() NOT NULL,
    effective_to timestamp with time zone,
    source text DEFAULT 'manual'::text NOT NULL,
    confidence numeric(4,3) DEFAULT 1.000,
    scraped_url text,
    offer_scope_key text GENERATED ALWAYS AS (((((((((((scope || ':'::text) || COALESCE((provider_id)::text, '-'::text)) || ':'::text) || COALESCE((credential_id)::text, '-'::text)) || ':'::text) || COALESCE(tenant_id, '-'::text)) || ':'::text) || COALESCE((model_canonical_id)::text, '-'::text)) || ':'::text) || plan_type)) STORED,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT pricing_plans_plan_type_check CHECK ((plan_type = ANY (ARRAY['token'::text, 'token_plan'::text, 'code_plan'::text, 'agent_plan'::text, 'request'::text, 'seat'::text, 'compute_time'::text, 'flat_quota'::text, 'free'::text]))),
    CONSTRAINT pricing_plans_scope_check CHECK ((scope = ANY (ARRAY['provider'::text, 'credential'::text, 'tenant'::text]))),
    CONSTRAINT pricing_plans_source_check CHECK ((source = ANY (ARRAY['manual'::text, 'seed'::text, 'litellm'::text, 'scraped'::text, 'catalog'::text])))
);


--
-- Name: COLUMN pricing_plans.plan_type; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_plans.plan_type IS 'Plan type: token (PAYG per-1M) | token_plan (prepaid credits/package, NEW 2026-06-12) | code_plan (subscription) | agent_plan (agent bundle) | seat (per seat) | request (per request) | compute_time | flat_quota | free';


--
-- Name: pricing_plans_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pricing_plans_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pricing_plans_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pricing_plans_id_seq OWNED BY public.pricing_plans.id;


--
-- Name: pricing_refresh_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.pricing_refresh_log (
    id bigint NOT NULL,
    run_id text NOT NULL,
    run_ts timestamp with time zone DEFAULT now() NOT NULL,
    trigger text DEFAULT 'cron'::text NOT NULL,
    status text NOT NULL,
    before_summary jsonb NOT NULL,
    after_summary jsonb NOT NULL,
    diff_count integer DEFAULT 0 NOT NULL,
    new_offers integer DEFAULT 0 NOT NULL,
    removed_offers integer DEFAULT 0 NOT NULL,
    changed_offers integer DEFAULT 0 NOT NULL,
    artifacts_path text,
    feishu_sent boolean DEFAULT false NOT NULL,
    error_message text,
    duration_seconds integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: TABLE pricing_refresh_log; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.pricing_refresh_log IS 'Audit log for monthly pricing refresh cron job. Each run inserts one row.';


--
-- Name: COLUMN pricing_refresh_log.before_summary; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.before_summary IS 'pricing/summary response BEFORE refresh (pricing_plans + cmb state)';


--
-- Name: COLUMN pricing_refresh_log.after_summary; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.after_summary IS 'pricing/summary response AFTER refresh';


--
-- Name: COLUMN pricing_refresh_log.diff_count; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.diff_count IS 'Total offers changed (new + removed + changed)';


--
-- Name: COLUMN pricing_refresh_log.artifacts_path; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.pricing_refresh_log.artifacts_path IS 'PVC path containing fetch.log, tier-pricing.csv, summary_*.json';


--
-- Name: pricing_refresh_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.pricing_refresh_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: pricing_refresh_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.pricing_refresh_log_id_seq OWNED BY public.pricing_refresh_log.id;


--
-- Name: provider_catalog; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_catalog (
    code text NOT NULL,
    tier text NOT NULL,
    display_name text NOT NULL,
    display_name_en text,
    category text DEFAULT 'official'::text NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    protocol text NOT NULL,
    base_url_template text NOT NULL,
    docs_url text,
    default_egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate_default numeric(5,4) DEFAULT 1.0,
    models_manifest_json jsonb DEFAULT '[]'::jsonb,
    discovery_strategy text DEFAULT 'auto'::text NOT NULL,
    models_endpoint_template text,
    seed_pricing_plans_json jsonb DEFAULT '[]'::jsonb,
    price_sources_json jsonb DEFAULT '{}'::jsonb,
    hidden boolean DEFAULT false NOT NULL,
    notes text,
    catalog_version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    header_profile_code text,
    capabilities jsonb DEFAULT '{}'::jsonb,
    vendor_name text,
    CONSTRAINT provider_catalog_category_check CHECK ((category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text]))),
    CONSTRAINT provider_catalog_discovery_strategy_check CHECK ((discovery_strategy = ANY (ARRAY['auto'::text, 'manifest'::text, 'hybrid'::text]))),
    CONSTRAINT provider_catalog_kind_check CHECK ((kind = ANY (ARRAY['cloud'::text, 'local'::text]))),
    CONSTRAINT provider_catalog_protocol_check CHECK ((protocol = ANY (ARRAY['openai-completions'::text, 'openai-responses'::text, 'anthropic-messages'::text, 'gemini-generate'::text, 'ollama-native'::text]))),
    CONSTRAINT provider_catalog_tier_check CHECK ((tier = ANY (ARRAY['tier1'::text, 'tier2'::text, 'local'::text, 'restricted'::text])))
);


--
-- Name: COLUMN provider_catalog.models_endpoint_template; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.models_endpoint_template IS '模型清单 API 模板：NULL=自动推导；/models 或 /v1/models 追加到 base_url；https://… 全 URL；空串=仅 manifest';


--
-- Name: COLUMN provider_catalog.capabilities; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.capabilities IS 'Per-catalog capability flags and request sanitization config';


--
-- Name: COLUMN provider_catalog.vendor_name; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.provider_catalog.vendor_name IS 'Human-readable vendor name for grouped view, e.g. "OpenAI", "Anthropic", "DeepSeek"';


--
-- Name: provider_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_events (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    event_kind text NOT NULL,
    payload_json jsonb,
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_events_id_seq OWNED BY public.provider_events.id;


--
-- Name: provider_header_profiles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_header_profiles (
    id bigint NOT NULL,
    profile_code text NOT NULL,
    display_name text NOT NULL,
    protocol text,
    headers_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    strip_headers_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_header_profiles_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_header_profiles_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_header_profiles_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_header_profiles_id_seq OWNED BY public.provider_header_profiles.id;


--
-- Name: provider_models_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_models_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_models_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_models_id_seq OWNED BY public.provider_models.id;


--
-- Name: provider_quality_rollup; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_quality_rollup (
    provider_id integer NOT NULL,
    bucket_start timestamp with time zone NOT NULL,
    total_requests integer DEFAULT 0 NOT NULL,
    bad_requests integer DEFAULT 0 NOT NULL,
    fixed_requests integer DEFAULT 0 NOT NULL,
    avg_quality_score numeric(3,2),
    top_flag text
);


--
-- Name: provider_scores; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_scores (
    id bigint NOT NULL,
    credential_id bigint NOT NULL,
    canonical_id bigint,
    score numeric(6,4) NOT NULL,
    factors_json jsonb,
    computed_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_scores_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_scores_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_scores_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_scores_id_seq OWNED BY public.provider_scores.id;


--
-- Name: provider_settings; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.provider_settings (
    id bigint NOT NULL,
    provider_id bigint NOT NULL,
    setting_key text NOT NULL,
    setting_value jsonb NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    created_by text DEFAULT 'system'::text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: provider_settings_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.provider_settings_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: provider_settings_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.provider_settings_id_seq OWNED BY public.provider_settings.id;


--
-- Name: providers; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.providers (
    id bigint NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    code text NOT NULL,
    display_name text NOT NULL,
    catalog_code text,
    is_custom boolean DEFAULT false NOT NULL,
    catalog_version_at_create integer,
    user_overrides_json jsonb DEFAULT '[]'::jsonb NOT NULL,
    kind text DEFAULT 'cloud'::text NOT NULL,
    category text DEFAULT 'official'::text NOT NULL,
    protocol text NOT NULL,
    base_url text NOT NULL,
    egress_profile text DEFAULT 'direct'::text NOT NULL,
    domestic boolean DEFAULT true NOT NULL,
    discount_rate numeric(5,4) DEFAULT 1.0,
    enabled boolean DEFAULT true NOT NULL,
    network_quality_score numeric(4,3) DEFAULT 1.000,
    owner_user text,
    notes text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    manual_disabled boolean DEFAULT false NOT NULL,
    quality_fix_mode text DEFAULT 'off'::text NOT NULL,
    CONSTRAINT providers_category_check CHECK ((category = ANY (ARRAY['official'::text, 'official_proxy'::text, 'third_party_relay'::text, 'aggregator'::text, 'self_host'::text]))),
    CONSTRAINT providers_kind_check CHECK ((kind = ANY (ARRAY['cloud'::text, 'local'::text]))),
    CONSTRAINT providers_quality_fix_mode_check CHECK ((quality_fix_mode = ANY (ARRAY['off'::text, 'detect_only'::text, 'fix'::text])))
);


--
-- Name: providers_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.providers_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: providers_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.providers_id_seq OWNED BY public.providers.id;


--
-- Name: request_envelope; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_envelope (
    request_id uuid NOT NULL,
    client_model text NOT NULL,
    client_metadata jsonb,
    client_headers_redacted jsonb,
    outbound_model text,
    outbound_protocol text,
    credential_id bigint,
    fingerprint_seed text,
    stream_chunks_sent integer DEFAULT 0 NOT NULL,
    stream_completed boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL
);


--
-- Name: request_logs_2026_04; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_04 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_05; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_05 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_06 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_07 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_2026_08; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_2026_08 (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_logs_default; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_logs_default (
    id bigint DEFAULT nextval('public.request_logs_id_seq'::regclass) NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone NOT NULL,
    tenant_id text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    client_model text,
    outbound_model text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    client_profile text,
    request_mode text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean NOT NULL,
    error_kind text,
    search_text text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    identity_hash text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    affinity_hit boolean,
    stream_first_chunk_ms integer,
    stream_chunk_count integer,
    stream_interrupted boolean,
    stream_done_sent boolean,
    request_checksum text,
    response_checksum text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    request_preview text,
    transform_summary text,
    response_preview text,
    stream_done_received boolean,
    request_body jsonb,
    response_body jsonb,
    cost_display numeric(14,8),
    cost_currency text,
    usage_source text DEFAULT 'llm'::text NOT NULL,
    gw_session_id text,
    gw_task_id text,
    request_status text,
    api_key_prefix text,
    owner_user text,
    application_code text,
    key_alias text,
    api_key_owner_user text,
    is_auto_request boolean DEFAULT false,
    task_type text,
    auto_profile text,
    auto_decision jsonb,
    auto_confidence numeric(4,3),
    work_type text,
    task_type_chosen text,
    confidence_num numeric(4,3),
    model_chosen text,
    strategy_used text,
    credits_charged bigint,
    parent_request_id text,
    compression_reason text,
    compression_strategy text,
    compression_meta jsonb,
    outbound_body jsonb,
    outbound_msg_count integer,
    outbound_token_est integer,
    outbound_msg_hashes jsonb,
    quality_flags text[] DEFAULT '{}'::text[] NOT NULL,
    quality_fix_actions jsonb DEFAULT '{}'::jsonb NOT NULL,
    quality_score numeric(3,2),
    upstream_finish_reason text,
    tool_calls jsonb,
    CONSTRAINT chk_compression_parent_single CHECK (((parent_request_id IS NULL) OR (compression_reason IS NOT NULL))),
    CONSTRAINT request_logs_strategy_used_check CHECK (((strategy_used IS NULL) OR (strategy_used = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text]))))
);


--
-- Name: request_wal; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb
)
PARTITION BY RANGE (created_at);


--
-- Name: request_wal_2026_06; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_2026_06 (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb
);


--
-- Name: request_wal_2026_07; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_2026_07 (
    request_id character varying(64) NOT NULL,
    tenant_id character varying(64) NOT NULL,
    gw_session_id character varying(128),
    status character varying(20) DEFAULT 'pending'::character varying NOT NULL,
    stage smallint DEFAULT 0 NOT NULL,
    client_model character varying(100),
    upstream_provider_id bigint,
    upstream_credential_id bigint,
    completion_tokens integer,
    prompt_tokens integer,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    upstream_request_at timestamp with time zone,
    upstream_response_at timestamp with time zone,
    error text,
    compression_strategy character varying(50),
    compression_meta jsonb
);


--
-- Name: request_wal_bodies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.request_wal_bodies (
    request_id character varying(64) NOT NULL,
    outbound_body text,
    compression_meta jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: route_decisions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.route_decisions (
    id bigint NOT NULL,
    request_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text,
    api_key_id bigint,
    canonical_id bigint,
    selected_credential_id bigint,
    candidates_json jsonb,
    reason text,
    sticky_hit boolean
);


--
-- Name: route_decisions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.route_decisions_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: route_decisions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.route_decisions_id_seq OWNED BY public.route_decisions.id;


--
-- Name: routing_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now(),
    actor text NOT NULL,
    action text NOT NULL,
    target_type text,
    target_id bigint,
    before_json jsonb,
    after_json jsonb
);


--
-- Name: routing_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_audit_log_id_seq OWNED BY public.routing_audit_log.id;


--
-- Name: routing_decision_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_decision_log (
    ts timestamp with time zone DEFAULT now() NOT NULL,
    request_id uuid NOT NULL,
    idempotency_key text,
    tenant_id text,
    api_key_id bigint,
    model text NOT NULL,
    chosen_credential_id bigint,
    chosen_provider_id bigint,
    tier smallint,
    candidates_tried smallint,
    latency_ms integer,
    success boolean NOT NULL,
    error_class text,
    prompt_tokens integer,
    completion_tokens integer,
    cost_usd numeric(12,6),
    request_bytes integer,
    response_bytes integer,
    client_model text,
    resolved_raw_model text,
    sticky_hit boolean,
    client_profile text,
    outbound_model text,
    request_mode text,
    identity_hash text,
    transform_rule_id text,
    egress_protocol text,
    failure_stage text,
    failure_detail_code text,
    virtual_client_id text,
    virtual_ip text,
    virtual_mac text,
    resolution_path text,
    canonical_model text,
    resolution_raw_models jsonb,
    decision_trace jsonb
);


--
-- Name: routing_overrides; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_overrides (
    id bigint NOT NULL,
    task_type text NOT NULL,
    profile text DEFAULT ''::text NOT NULL,
    mode text NOT NULL,
    model_chosen text,
    reason text DEFAULT ''::text NOT NULL,
    created_by text,
    expires_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT routing_overrides_mode_check CHECK ((mode = ANY (ARRAY['pin'::text, 'ban'::text])))
);


--
-- Name: routing_overrides_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_overrides_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    override_id bigint,
    task_type text,
    profile text,
    mode text,
    model_chosen text,
    reason text,
    expires_at timestamp with time zone,
    old_expires_at timestamp with time zone,
    actor text,
    CONSTRAINT routing_overrides_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text])))
);


--
-- Name: routing_overrides_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_overrides_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_overrides_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_overrides_audit_id_seq OWNED BY public.routing_overrides_audit.id;


--
-- Name: routing_overrides_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.routing_overrides_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: routing_overrides_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.routing_overrides_id_seq OWNED BY public.routing_overrides.id;


--
-- Name: routing_policy; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.routing_policy (
    id smallint DEFAULT 1 NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    weights_json jsonb DEFAULT '{}'::jsonb NOT NULL,
    sticky_ttl_seconds integer DEFAULT 1800 NOT NULL,
    local_bonus numeric(4,3) DEFAULT 0.000 NOT NULL,
    notes text,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    algorithm_version smallint DEFAULT 2,
    retry_per_credential smallint DEFAULT 1,
    tier_fallback_max smallint DEFAULT 4,
    slot_soft_limit_ratio numeric(3,2) DEFAULT 1.00,
    slot_hard_limit_ratio numeric(3,2) DEFAULT 1.50,
    slot_wait_max_ms smallint DEFAULT 200,
    circuit_open_seconds integer DEFAULT 300,
    circuit_failure_threshold smallint DEFAULT 5,
    circuit_max_open_seconds integer DEFAULT 1800,
    featured_models text[] DEFAULT ARRAY['gpt-4o'::text, 'gpt-4o-mini'::text, 'claude-3-5-sonnet-20241022'::text, 'claude-3-7-sonnet-20250219'::text, 'gemini-2.0-flash'::text, 'gemini-1.5-pro'::text, 'deepseek-chat'::text, 'qwen-plus'::text],
    transient_fail_threshold integer DEFAULT 2 NOT NULL,
    stats_window_minutes integer DEFAULT 10,
    stats_update_interval_seconds integer DEFAULT 60,
    scoring_weights_json jsonb DEFAULT '{"price": 10, "session_load": 5, "failure_penalty": 20, "default_price_cny": 5.0, "default_price_usd": 5.0}'::jsonb,
    CONSTRAINT routing_policy_id_check CHECK ((id = 1)),
    CONSTRAINT routing_policy_transient_fail_threshold_check CHECK (((transient_fail_threshold >= 0) AND (transient_fail_threshold <= 10)))
);


--
-- Name: schema_migration_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migration_audit (
    migration_id text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL,
    row_count bigint DEFAULT 0 NOT NULL,
    note text DEFAULT ''::text NOT NULL
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version text NOT NULL,
    description text,
    applied_at timestamp with time zone DEFAULT now()
);


--
-- Name: security_audit_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.security_audit_log (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    event_kind text NOT NULL,
    api_key_id bigint,
    internal_service_id text,
    actor text,
    tenant_id text,
    remote_ip inet,
    detail_json jsonb,
    CONSTRAINT security_audit_log_event_kind_check CHECK ((event_kind = ANY (ARRAY['key_created'::text, 'key_disabled'::text, 'key_throttled'::text, 'key_unthrottled'::text, 'key_revoked'::text, 'key_revealed'::text, 'auth_failed'::text, 'auth_expired'::text, 'admin_login_failed'::text, 'key_reencrypted'::text, 'hmac_sig_failed'::text, 'hmac_nonce_replay'::text, 'hmac_timestamp_bad'::text, 'rate_limited'::text, 'anomaly_spike'::text])))
);


--
-- Name: security_audit_log_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.security_audit_log_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: security_audit_log_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.security_audit_log_id_seq OWNED BY public.security_audit_log.id;


--
-- Name: session_memora_extraction_log; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_memora_extraction_log (
    task_id text NOT NULL,
    extracted_at timestamp with time zone DEFAULT now() NOT NULL,
    written integer DEFAULT 0 NOT NULL,
    skipped_noise integer DEFAULT 0 NOT NULL,
    skipped_duplicate integer DEFAULT 0 NOT NULL,
    status text DEFAULT 'ok'::text NOT NULL,
    detail jsonb
);


--
-- Name: session_titles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_titles (
    task_id text NOT NULL,
    scoped_session_id text DEFAULT ''::text NOT NULL,
    title text NOT NULL,
    generated_at timestamp with time zone DEFAULT now() NOT NULL,
    model text,
    api_key_id integer
);


--
-- Name: settings_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_audit (
    id bigint NOT NULL,
    setting_key character varying(128) NOT NULL,
    tenant_id character varying(64),
    action character varying(16) NOT NULL,
    old_value jsonb,
    new_value jsonb,
    operator_user character varying(64) NOT NULL,
    operator_role character varying(32) NOT NULL,
    confirm_token character varying(64),
    client_ip character varying(45),
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.settings_audit FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE settings_audit; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_audit IS '设置修改审计日志（bg/settings_audit_cleaner.go 每 24h 清理 7 天前的数据）';


--
-- Name: COLUMN settings_audit.action; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.settings_audit.action IS 'update / rollback / delete';


--
-- Name: settings_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.settings_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: settings_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.settings_audit_id_seq OWNED BY public.settings_audit.id;


--
-- Name: settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.settings_kv (
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    scope character varying(16) DEFAULT 'platform'::character varying NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);


--
-- Name: TABLE settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.settings_kv IS '平台级运行时设置（Q2: 立即生效）';


--
-- Name: COLUMN settings_kv.prev_value; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.settings_kv.prev_value IS '上次的值，用于一键回滚';


--
-- Name: sticky_sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sticky_sessions (
    sticky_key text NOT NULL,
    credential_id bigint NOT NULL,
    set_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    canonical_id bigint,
    last_request_id text
);


--
-- Name: subscription_plans; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.subscription_plans (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    tier character varying(16) NOT NULL,
    name character varying(128) NOT NULL,
    price_cents integer NOT NULL,
    monthly_credits bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT subscription_plans_tier_check CHECK (((tier)::text = ANY (ARRAY[('basic'::character varying)::text, ('pro'::character varying)::text, ('max'::character varying)::text])))
);


--
-- Name: subscription_plans_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.subscription_plans_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: subscription_plans_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.subscription_plans_id_seq OWNED BY public.subscription_plans.id;


--
-- Name: system_identity_pool; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.system_identity_pool (
    id integer DEFAULT 1 NOT NULL,
    max_identities integer DEFAULT 10000 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by text,
    CONSTRAINT system_identity_pool_id_check CHECK ((id = 1))
);


--
-- Name: TABLE system_identity_pool; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.system_identity_pool IS 'Global cap on total distinct end-user identities the gateway will accept. Once this many unique fingerprints are active, new connections must reuse an existing fingerprint (round-robin among least-recently-used).';


--
-- Name: tenant_credit_wallets; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_credit_wallets (
    tenant_id character varying(64) NOT NULL,
    balance_credits bigint DEFAULT 0 NOT NULL,
    locked_credits bigint DEFAULT 0 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    granted_balance bigint DEFAULT 0 NOT NULL,
    purchased_balance bigint DEFAULT 0 NOT NULL
);


--
-- Name: tenant_model_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_model_policies (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    canonical_name text NOT NULL,
    reason text DEFAULT ''::text NOT NULL,
    created_by character varying(128) DEFAULT ''::character varying NOT NULL,
    deleted_at timestamp with time zone,
    deleted_by character varying(128),
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_model_policies_canonical_name_check CHECK ((canonical_name <> ''::text))
);

ALTER TABLE ONLY public.tenant_model_policies FORCE ROW LEVEL SECURITY;


--
-- Name: tenant_model_policies_active; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.tenant_model_policies_active AS
 SELECT tenant_model_policies.id,
    tenant_model_policies.tenant_id,
    tenant_model_policies.canonical_name,
    tenant_model_policies.reason,
    tenant_model_policies.created_by,
    tenant_model_policies.created_at,
    tenant_model_policies.updated_at
   FROM public.tenant_model_policies
  WHERE (tenant_model_policies.deleted_at IS NULL);


--
-- Name: tenant_model_policies_audit; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_model_policies_audit (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    action text NOT NULL,
    policy_id bigint,
    tenant_id text,
    canonical_name text,
    reason text,
    actor text,
    CONSTRAINT tenant_model_policies_audit_action_check CHECK ((action = ANY (ARRAY['insert'::text, 'update'::text, 'delete'::text, 'undelete'::text])))
);

ALTER TABLE ONLY public.tenant_model_policies_audit FORCE ROW LEVEL SECURITY;


--
-- Name: tenant_model_policies_audit_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_model_policies_audit_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_model_policies_audit_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_model_policies_audit_id_seq OWNED BY public.tenant_model_policies_audit.id;


--
-- Name: tenant_model_policies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_model_policies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_model_policies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_model_policies_id_seq OWNED BY public.tenant_model_policies.id;


--
-- Name: tenant_settings_kv; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_settings_kv (
    tenant_id character varying(64) NOT NULL,
    key character varying(128) NOT NULL,
    value jsonb NOT NULL,
    value_type character varying(32) NOT NULL,
    category character varying(32) DEFAULT 'general'::character varying NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_by character varying(64),
    prev_value jsonb,
    prev_updated_at timestamp with time zone
);

ALTER TABLE ONLY public.tenant_settings_kv FORCE ROW LEVEL SECURITY;


--
-- Name: TABLE tenant_settings_kv; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tenant_settings_kv IS '租户级运行时设置（Q3）';


--
-- Name: tenant_subscriptions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_subscriptions (
    id integer NOT NULL,
    tenant_id character varying(64) NOT NULL,
    plan_id integer NOT NULL,
    status character varying(32) DEFAULT 'active'::character varying NOT NULL,
    period_start timestamp with time zone NOT NULL,
    period_end timestamp with time zone NOT NULL,
    quota_remaining bigint DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenant_subscriptions_status_check CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('active'::character varying)::text, ('expired'::character varying)::text, ('cancelled'::character varying)::text])))
);


--
-- Name: tenant_subscriptions_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_subscriptions_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_subscriptions_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_subscriptions_id_seq OWNED BY public.tenant_subscriptions.id;


--
-- Name: tenant_tool_policies; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenant_tool_policies (
    id bigint NOT NULL,
    tenant_id character varying(64) NOT NULL,
    tool_pattern character varying(128) NOT NULL,
    policy_type character varying(16) NOT NULL,
    reason character varying(256),
    enabled boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    created_by character varying(128),
    CONSTRAINT chk_policy_type CHECK (((policy_type)::text = ANY (ARRAY[('allow'::character varying)::text, ('deny'::character varying)::text])))
);

ALTER TABLE ONLY public.tenant_tool_policies FORCE ROW LEVEL SECURITY;


--
-- Name: tenant_tool_policies_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tenant_tool_policies_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tenant_tool_policies_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tenant_tool_policies_id_seq OWNED BY public.tenant_tool_policies.id;


--
-- Name: tenants; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tenants (
    code character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    status character varying(32) DEFAULT 'active'::character varying NOT NULL,
    description text DEFAULT ''::text NOT NULL,
    contact_email character varying(256) DEFAULT ''::character varying NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tenants_status_check CHECK (((status)::text = ANY (ARRAY[('active'::character varying)::text, ('trial'::character varying)::text, ('suspended'::character varying)::text, ('expired'::character varying)::text, ('disabled'::character varying)::text])))
);


SET default_table_access_method = columnar;

--
-- Name: test_columnar_new; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.test_columnar_new (
    id integer NOT NULL,
    tenant_id text,
    model text,
    prompt_tokens integer,
    completion_tokens integer,
    created_at timestamp with time zone DEFAULT now()
);


SET default_table_access_method = heap;

--
-- Name: token_audit_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.token_audit_events (
    id bigint NOT NULL,
    request_id text NOT NULL,
    credential_id bigint NOT NULL,
    claimed_tokens integer,
    estimated_tokens integer,
    delta_pct numeric(6,3),
    ts timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: token_audit_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.token_audit_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: token_audit_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.token_audit_events_id_seq OWNED BY public.token_audit_events.id;


--
-- Name: tool_call_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_call_events (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    request_id character varying(64),
    api_key character varying(64),
    status character varying(16) NOT NULL,
    latency_ms integer DEFAULT 0,
    error_code character varying(64),
    called_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT chk_status CHECK (((status)::text = ANY (ARRAY[('success'::character varying)::text, ('error'::character varying)::text, ('timeout'::character varying)::text])))
);

ALTER TABLE ONLY public.tool_call_events FORCE ROW LEVEL SECURITY;


--
-- Name: tool_call_events_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_call_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_call_events_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_call_events_id_seq OWNED BY public.tool_call_events.id;


--
-- Name: tool_categories; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_categories (
    id character varying(64) NOT NULL,
    name character varying(128) NOT NULL,
    description text,
    enabled boolean DEFAULT true,
    display_order integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now()
);


--
-- Name: TABLE tool_categories; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_categories IS 'Phase 2: Tool category definitions for layered loading';


--
-- Name: tool_registry; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_registry (
    id integer NOT NULL,
    category character varying(64) NOT NULL,
    tool_name character varying(128) NOT NULL,
    tool_definition jsonb NOT NULL,
    enabled boolean DEFAULT true,
    priority integer DEFAULT 0,
    created_at timestamp with time zone DEFAULT now(),
    updated_at timestamp with time zone DEFAULT now(),
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying,
    version integer DEFAULT 1,
    deprecation_date timestamp with time zone,
    min_client_version character varying(32),
    breaking_changes jsonb DEFAULT '[]'::jsonb,
    superseded_by character varying(128)
);


--
-- Name: TABLE tool_registry; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON TABLE public.tool_registry IS 'Phase 2: Centralized tool definition registry';


--
-- Name: COLUMN tool_registry.tool_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.tool_id IS 'Phase 3: Unique tool identifier (category.tool_name)';


--
-- Name: COLUMN tool_registry.tenant_id; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.tenant_id IS 'Phase 3: Tenant isolation (default = global shared)';


--
-- Name: COLUMN tool_registry.version; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.tool_registry.version IS 'Phase 3: Tool version (fixed at 1 for Phase 3.0)';


--
-- Name: tool_registry_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_registry_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_registry_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_registry_id_seq OWNED BY public.tool_registry.id;


--
-- Name: tool_usage_stats; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tool_usage_stats (
    id bigint NOT NULL,
    tool_id character varying(128) NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    usage_date date DEFAULT CURRENT_DATE NOT NULL,
    call_count bigint DEFAULT 0 NOT NULL,
    success_count bigint DEFAULT 0 NOT NULL,
    error_count bigint DEFAULT 0 NOT NULL,
    avg_latency_ms integer DEFAULT 0,
    last_called_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.tool_usage_stats FORCE ROW LEVEL SECURITY;


--
-- Name: tool_usage_stats_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tool_usage_stats_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tool_usage_stats_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tool_usage_stats_id_seq OWNED BY public.tool_usage_stats.id;


--
-- Name: topup_packages; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.topup_packages (
    id integer NOT NULL,
    code character varying(32) NOT NULL,
    tier character varying(16) NOT NULL,
    name character varying(128) NOT NULL,
    price_cents integer NOT NULL,
    credits_amount bigint NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT topup_packages_tier_check CHECK (((tier)::text = ANY (ARRAY[('small'::character varying)::text, ('medium'::character varying)::text, ('large'::character varying)::text])))
);


--
-- Name: topup_packages_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.topup_packages_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: topup_packages_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.topup_packages_id_seq OWNED BY public.topup_packages.id;


--
-- Name: tuning_params; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_params (
    key text NOT NULL,
    value jsonb NOT NULL,
    category text NOT NULL,
    source text DEFAULT 'default'::text NOT NULL,
    confidence numeric(4,3) DEFAULT 1.0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    description text,
    applied_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: tuning_proposals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_proposals (
    id bigint NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    category text NOT NULL,
    task_type text,
    proposal jsonb NOT NULL,
    evidence jsonb NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    reviewed_by text,
    reviewed_at timestamp with time zone,
    applied_at timestamp with time zone,
    review_note text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT tuning_proposals_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'approved'::text, 'rejected'::text, 'applied'::text, 'expired'::text])))
);


--
-- Name: tuning_proposals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tuning_proposals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tuning_proposals_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tuning_proposals_id_seq OWNED BY public.tuning_proposals.id;


--
-- Name: tuning_signals; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.tuning_signals (
    id bigint NOT NULL,
    request_id text NOT NULL,
    session_id text,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    task_type text NOT NULL,
    classifier text NOT NULL,
    confidence numeric(4,3),
    chosen_model text,
    canonical_id integer,
    success_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    cost_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    drift_flag boolean DEFAULT false NOT NULL,
    quality_score numeric(3,2) DEFAULT 0.5 NOT NULL,
    latency_ms integer,
    cost_usd numeric(10,6),
    prompt_tokens integer,
    completion_tokens integer,
    signal_payload jsonb,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    strategy text DEFAULT 'pattern_layered'::text NOT NULL,
    CONSTRAINT tuning_signals_strategy_check CHECK ((strategy = ANY (ARRAY['baseline_heuristic'::text, 'pattern_layered'::text, 'llm_fallback'::text])))
);


--
-- Name: tuning_signals_5m; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_5m AS
 SELECT (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)) AS bucket,
    tuning_signals.task_type,
    tuning_signals.classifier,
    count(*) AS total,
    avg(tuning_signals.quality_score) AS avg_quality,
    avg(tuning_signals.success_score) AS avg_success,
    avg(tuning_signals.latency_score) AS avg_latency,
    avg(tuning_signals.cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN tuning_signals.drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (tuning_signals.ts >= (now() - '7 days'::interval))
  GROUP BY (date_trunc('hour'::text, tuning_signals.ts) + (floor((((EXTRACT(minute FROM tuning_signals.ts))::integer / 5))::double precision) * '00:05:00'::interval)), tuning_signals.task_type, tuning_signals.classifier
  WITH NO DATA;


--
-- Name: tuning_signals_daily; Type: MATERIALIZED VIEW; Schema: public; Owner: -
--

CREATE MATERIALIZED VIEW public.tuning_signals_daily AS
 SELECT date_trunc('day'::text, tuning_signals.ts) AS bucket,
    tuning_signals.task_type,
    tuning_signals.classifier,
    count(*) AS total,
    avg(tuning_signals.quality_score) AS avg_quality,
    avg(tuning_signals.success_score) AS avg_success,
    avg(tuning_signals.latency_score) AS avg_latency,
    avg(tuning_signals.cost_score) AS avg_cost,
    ((sum(
        CASE
            WHEN tuning_signals.drift_flag THEN 1
            ELSE 0
        END))::double precision / (NULLIF(count(*), 0))::double precision) AS drift_rate
   FROM public.tuning_signals
  WHERE (tuning_signals.ts >= (now() - '90 days'::interval))
  GROUP BY (date_trunc('day'::text, tuning_signals.ts)), tuning_signals.task_type, tuning_signals.classifier
  WITH NO DATA;


--
-- Name: tuning_signals_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.tuning_signals_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: tuning_signals_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.tuning_signals_id_seq OWNED BY public.tuning_signals.id;


--
-- Name: usage_ledger; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_ledger (
    id bigint NOT NULL,
    request_id text NOT NULL,
    ts timestamp with time zone DEFAULT now() NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    raw_model_name text,
    prompt_tokens integer,
    completion_tokens integer,
    total_tokens integer,
    cost_usd numeric(14,8),
    latency_ms integer,
    success boolean,
    error_kind text,
    route_reason text,
    cache_read_tokens integer,
    cache_write_tokens integer,
    cost_currency text
);


--
-- Name: COLUMN usage_ledger.cost_currency; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON COLUMN public.usage_ledger.cost_currency IS 'Currency for usage_ledger.cost_usd source pricing; USD when cost_usd is directly billable.';


--
-- Name: usage_ledger_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.usage_ledger_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: usage_ledger_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.usage_ledger_id_seq OWNED BY public.usage_ledger.id;


--
-- Name: usage_minute; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.usage_minute (
    bucket timestamp with time zone NOT NULL,
    tenant_id text DEFAULT 'default'::text NOT NULL,
    application_id bigint,
    api_key_id bigint,
    end_user_id text,
    department text,
    employee text,
    "position" text,
    credential_id bigint,
    provider_id bigint,
    canonical_id bigint,
    requests bigint DEFAULT 0 NOT NULL,
    prompt_tokens bigint DEFAULT 0 NOT NULL,
    completion_tokens bigint DEFAULT 0 NOT NULL,
    total_tokens bigint DEFAULT 0 NOT NULL,
    cost_usd numeric(18,8) DEFAULT 0 NOT NULL,
    errors bigint DEFAULT 0 NOT NULL
);


--
-- Name: users; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.users (
    id integer NOT NULL,
    tenant_id character varying(64) DEFAULT 'default'::character varying NOT NULL,
    username character varying(128) NOT NULL,
    password_hash character varying(256) NOT NULL,
    display_name character varying(128) DEFAULT ''::character varying NOT NULL,
    email character varying(256) DEFAULT ''::character varying NOT NULL,
    role character varying(32) DEFAULT 'tenant_admin'::character varying NOT NULL,
    enabled boolean DEFAULT true NOT NULL,
    last_login_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: users_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.users_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: users_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.users_id_seq OWNED BY public.users.id;


--
-- Name: v_fp_slot_policy; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_fp_slot_policy AS
 SELECT COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::boolean AS bool
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_enabled'::text)), true) AS enabled,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_max_per_credential'::text)), 100) AS max_per_credential,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::numeric AS "numeric"
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_default_ratio'::text)), 0.25) AS default_ratio,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_client_fingerprint_ttl_days'::text)), 30) AS client_ttl_days,
    COALESCE(( SELECT ((settings_kv.value #>> '{}'::text[]))::integer AS int4
           FROM public.settings_kv
          WHERE ((settings_kv.key)::text = 'llmgw_fp_slot_max_total_clients'::text)), 10000) AS max_total_clients;


--
-- Name: VIEW v_fp_slot_policy; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_fp_slot_policy IS 'Active fingerprint-slot policy derived from settings_kv. Used by admin UI and the credentialfpslot manager at boot.';


--
-- Name: v_recent_model_probe_failures; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_recent_model_probe_failures AS
 SELECT model_probe_runs.raw_model_name,
    model_probe_runs.credential_id,
    count(*) AS failed_count,
    max(model_probe_runs.created_at) AS last_failed_at,
    min(model_probe_runs.error_code) AS sample_error_code
   FROM public.model_probe_runs
  WHERE ((model_probe_runs.status <> 'ok'::text) AND (model_probe_runs.status <> 'skipped'::text) AND (model_probe_runs.created_at > (now() - '06:00:00'::interval)))
  GROUP BY model_probe_runs.raw_model_name, model_probe_runs.credential_id;


--
-- Name: v_routable_credential_models; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_routable_credential_models AS
 SELECT cmb.id AS binding_id,
    cmb.credential_id,
    cmb.provider_model_id,
    c.tenant_id,
    p.id AS provider_id,
    c.label AS credential_label,
    pm.raw_model_name,
    pm.canonical_id,
        CASE
            WHEN (NOT p.enabled) THEN 'provider_disabled'::text
            WHEN COALESCE(p.manual_disabled, false) THEN 'provider_manual_disabled'::text
            WHEN (c.status <> 'active'::text) THEN ('credential_status_'::text || c.status)
            WHEN (c.lifecycle_status <> 'active'::text) THEN ('lifecycle_'::text || c.lifecycle_status)
            WHEN COALESCE(c.manual_disabled, false) THEN 'credential_manual_disabled'::text
            WHEN (c.availability_state = 'cooling'::text) THEN 'availability_cooling'::text
            WHEN (c.availability_state = 'rate_limited'::text) THEN 'availability_rate_limited'::text
            WHEN (c.availability_state = 'auth_failed'::text) THEN 'availability_auth_failed'::text
            WHEN (c.availability_state = 'unreachable'::text) THEN 'availability_unreachable'::text
            WHEN (c.availability_state = 'suspended'::text) THEN 'availability_suspended'::text
            WHEN (c.quota_state = ANY (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) THEN ('quota_'::text || c.quota_state)
            WHEN ((c.health_status = 'unreachable'::text) AND (c.health_checked_at > (now() - '01:00:00'::interval))) THEN 'recent_probe_unreachable'::text
            WHEN (NOT pm.available) THEN 'model_unavailable'::text
            WHEN (cmb.unavailable_reason = 'manual'::text) THEN 'model_manual_disabled'::text
            WHEN (NOT cmb.available) THEN 'binding_unavailable'::text
            ELSE NULL::text
        END AS unavailable_reason,
    (p.enabled AND (COALESCE(p.manual_disabled, false) = false) AND (c.status = 'active'::text) AND (c.lifecycle_status = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (c.availability_state = 'ready'::text) AND (c.quota_state <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) AND (pm.available = true) AND (cmb.available = true) AND (cmb.unavailable_reason IS DISTINCT FROM 'manual'::text) AND (COALESCE(c.health_status, 'unknown'::text) = ANY (ARRAY['healthy'::text, 'unknown'::text]))) AS is_routable,
    (((((cmb.manual_priority * 100))::numeric + (COALESCE(cmb.success_rate, 0.5) * (50)::numeric)) - (COALESCE(cmb.unit_price_in_per_1m, (0)::numeric) * 0.001)) - ((COALESCE(cmb.p95_latency_ms, 1000))::numeric * 0.01)) AS routing_score
   FROM (((public.credential_model_bindings cmb
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)));


--
-- Name: work_type_config; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_type_config (
    key text NOT NULL,
    label text NOT NULL,
    category text NOT NULL,
    l1_task_type text NOT NULL,
    default_profile text DEFAULT 'smart'::text NOT NULL,
    tags text[] DEFAULT '{}'::text[] NOT NULL,
    prompt_keywords text[] DEFAULT '{}'::text[] NOT NULL,
    acc_task_type text,
    enabled boolean DEFAULT true NOT NULL,
    sort_order integer DEFAULT 0 NOT NULL,
    synced_from_acc_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    system_prompt text,
    CONSTRAINT work_type_config_default_profile_check CHECK ((default_profile = ANY (ARRAY['smart'::text, 'speed_first'::text, 'cost_first'::text])))
);


--
-- Name: work_type_model_route; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.work_type_model_route (
    id integer NOT NULL,
    work_type_key text NOT NULL,
    canonical_name text NOT NULL,
    weight numeric(5,2) DEFAULT 1.0 NOT NULL,
    min_score numeric(8,4) DEFAULT 0 NOT NULL,
    enabled boolean DEFAULT true NOT NULL
);


--
-- Name: work_type_model_route_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.work_type_model_route_id_seq
    AS integer
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: work_type_model_route_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.work_type_model_route_id_seq OWNED BY public.work_type_model_route.id;


--
-- Name: request_logs_2026_04; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_04 FOR VALUES FROM ('2026-04-01 00:00:00+00') TO ('2026-05-01 00:00:00+00');


--
-- Name: request_logs_2026_05; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_05 FOR VALUES FROM ('2026-05-01 00:00:00+00') TO ('2026-06-01 00:00:00+00');


--
-- Name: request_logs_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: request_logs_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: request_logs_2026_08; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_2026_08 FOR VALUES FROM ('2026-08-01 00:00:00+00') TO ('2026-09-01 00:00:00+00');


--
-- Name: request_logs_default; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs ATTACH PARTITION public.request_logs_default DEFAULT;


--
-- Name: request_wal_2026_06; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal ATTACH PARTITION public.request_wal_2026_06 FOR VALUES FROM ('2026-06-01 00:00:00+00') TO ('2026-07-01 00:00:00+00');


--
-- Name: request_wal_2026_07; Type: TABLE ATTACH; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal ATTACH PARTITION public.request_wal_2026_07 FOR VALUES FROM ('2026-07-01 00:00:00+00') TO ('2026-08-01 00:00:00+00');


--
-- Name: api_keys id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys ALTER COLUMN id SET DEFAULT nextval('public.api_keys_id_seq'::regclass);


--
-- Name: applications id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications ALTER COLUMN id SET DEFAULT nextval('public.applications_id_seq'::regclass);


--
-- Name: auto_tune_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auto_tune_audit ALTER COLUMN id SET DEFAULT nextval('public.auto_tune_audit_id_seq'::regclass);


--
-- Name: background_tasks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_tasks ALTER COLUMN id SET DEFAULT nextval('public.background_tasks_id_seq'::regclass);


--
-- Name: billing_orders id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.billing_orders ALTER COLUMN id SET DEFAULT nextval('public.billing_orders_id_seq'::regclass);


--
-- Name: candidate_failure_logs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.candidate_failure_logs ALTER COLUMN id SET DEFAULT nextval('public.candidate_failure_logs_id_seq'::regclass);


--
-- Name: credential_capabilities id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_capabilities ALTER COLUMN id SET DEFAULT nextval('public.credential_capabilities_id_seq'::regclass);


--
-- Name: credential_health_checks id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_health_checks ALTER COLUMN id SET DEFAULT nextval('public.credential_health_checks_id_seq'::regclass);


--
-- Name: credential_model_bindings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings ALTER COLUMN id SET DEFAULT nextval('public.credential_model_bindings_id_seq'::regclass);


--
-- Name: credential_probe_model_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probe_model_log ALTER COLUMN id SET DEFAULT nextval('public.credential_probe_model_log_id_seq'::regclass);


--
-- Name: credential_quota_usage id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage ALTER COLUMN id SET DEFAULT nextval('public.credential_quota_usage_id_seq'::regclass);


--
-- Name: credential_quotas id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quotas ALTER COLUMN id SET DEFAULT nextval('public.credential_quotas_id_seq'::regclass);


--
-- Name: credentials id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials ALTER COLUMN id SET DEFAULT nextval('public.credentials_id_seq'::regclass);


--
-- Name: credit_ledger id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger ALTER COLUMN id SET DEFAULT nextval('public.credit_ledger_id_seq'::regclass);


--
-- Name: local_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models ALTER COLUMN id SET DEFAULT nextval('public.local_models_id_seq'::regclass);


--
-- Name: local_runtimes id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes ALTER COLUMN id SET DEFAULT nextval('public.local_runtimes_id_seq'::regclass);


--
-- Name: model_aliases id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_aliases ALTER COLUMN id SET DEFAULT nextval('public.model_aliases_id_seq'::regclass);


--
-- Name: model_discovery_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_discovery_runs ALTER COLUMN id SET DEFAULT nextval('public.model_discovery_runs_id_seq'::regclass);


--
-- Name: model_fingerprints id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_fingerprints ALTER COLUMN id SET DEFAULT nextval('public.model_fingerprints_id_seq'::regclass);


--
-- Name: model_lifecycle_jobs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_lifecycle_jobs ALTER COLUMN id SET DEFAULT nextval('public.model_lifecycle_jobs_id_seq'::regclass);


--
-- Name: model_offer_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offer_events ALTER COLUMN id SET DEFAULT nextval('public.model_offer_events_id_seq'::regclass);


--
-- Name: model_offers_legacy id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy ALTER COLUMN id SET DEFAULT nextval('public.model_offers_id_seq'::regclass);


--
-- Name: model_probe_runs id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_runs ALTER COLUMN id SET DEFAULT nextval('public.model_probe_runs_id_seq'::regclass);


--
-- Name: model_reconcile_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_reconcile_log ALTER COLUMN id SET DEFAULT nextval('public.model_reconcile_log_id_seq'::regclass);


--
-- Name: models_canonical id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical ALTER COLUMN id SET DEFAULT nextval('public.models_canonical_id_seq'::regclass);


--
-- Name: ops_model_offers_backup backup_id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_model_offers_backup ALTER COLUMN backup_id SET DEFAULT nextval('public.ops_model_offers_backup_backup_id_seq'::regclass);


--
-- Name: price_change_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.price_change_events ALTER COLUMN id SET DEFAULT nextval('public.price_change_events_id_seq'::regclass);


--
-- Name: pricing_plans id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_plans ALTER COLUMN id SET DEFAULT nextval('public.pricing_plans_id_seq'::regclass);


--
-- Name: pricing_refresh_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_refresh_log ALTER COLUMN id SET DEFAULT nextval('public.pricing_refresh_log_id_seq'::regclass);


--
-- Name: provider_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_events ALTER COLUMN id SET DEFAULT nextval('public.provider_events_id_seq'::regclass);


--
-- Name: provider_header_profiles id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles ALTER COLUMN id SET DEFAULT nextval('public.provider_header_profiles_id_seq'::regclass);


--
-- Name: provider_models id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models ALTER COLUMN id SET DEFAULT nextval('public.provider_models_id_seq'::regclass);


--
-- Name: provider_scores id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_scores ALTER COLUMN id SET DEFAULT nextval('public.provider_scores_id_seq'::regclass);


--
-- Name: provider_settings id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings ALTER COLUMN id SET DEFAULT nextval('public.provider_settings_id_seq'::regclass);


--
-- Name: providers id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers ALTER COLUMN id SET DEFAULT nextval('public.providers_id_seq'::regclass);


--
-- Name: route_decisions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.route_decisions ALTER COLUMN id SET DEFAULT nextval('public.route_decisions_id_seq'::regclass);


--
-- Name: routing_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_audit_log ALTER COLUMN id SET DEFAULT nextval('public.routing_audit_log_id_seq'::regclass);


--
-- Name: routing_overrides id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides ALTER COLUMN id SET DEFAULT nextval('public.routing_overrides_id_seq'::regclass);


--
-- Name: routing_overrides_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides_audit ALTER COLUMN id SET DEFAULT nextval('public.routing_overrides_audit_id_seq'::regclass);


--
-- Name: security_audit_log id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_audit_log ALTER COLUMN id SET DEFAULT nextval('public.security_audit_log_id_seq'::regclass);


--
-- Name: settings_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_audit ALTER COLUMN id SET DEFAULT nextval('public.settings_audit_id_seq'::regclass);


--
-- Name: subscription_plans id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_plans ALTER COLUMN id SET DEFAULT nextval('public.subscription_plans_id_seq'::regclass);


--
-- Name: tenant_model_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_id_seq'::regclass);


--
-- Name: tenant_model_policies_audit id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies_audit ALTER COLUMN id SET DEFAULT nextval('public.tenant_model_policies_audit_id_seq'::regclass);


--
-- Name: tenant_subscriptions id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_subscriptions ALTER COLUMN id SET DEFAULT nextval('public.tenant_subscriptions_id_seq'::regclass);


--
-- Name: tenant_tool_policies id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_tool_policies ALTER COLUMN id SET DEFAULT nextval('public.tenant_tool_policies_id_seq'::regclass);


--
-- Name: token_audit_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_audit_events ALTER COLUMN id SET DEFAULT nextval('public.token_audit_events_id_seq'::regclass);


--
-- Name: tool_call_events id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_call_events ALTER COLUMN id SET DEFAULT nextval('public.tool_call_events_id_seq'::regclass);


--
-- Name: tool_registry id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry ALTER COLUMN id SET DEFAULT nextval('public.tool_registry_id_seq'::regclass);


--
-- Name: tool_usage_stats id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats ALTER COLUMN id SET DEFAULT nextval('public.tool_usage_stats_id_seq'::regclass);


--
-- Name: topup_packages id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages ALTER COLUMN id SET DEFAULT nextval('public.topup_packages_id_seq'::regclass);


--
-- Name: tuning_proposals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_proposals ALTER COLUMN id SET DEFAULT nextval('public.tuning_proposals_id_seq'::regclass);


--
-- Name: tuning_signals id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_signals ALTER COLUMN id SET DEFAULT nextval('public.tuning_signals_id_seq'::regclass);


--
-- Name: usage_ledger id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger ALTER COLUMN id SET DEFAULT nextval('public.usage_ledger_id_seq'::regclass);


--
-- Name: users id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users ALTER COLUMN id SET DEFAULT nextval('public.users_id_seq'::regclass);


--
-- Name: work_type_model_route id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route ALTER COLUMN id SET DEFAULT nextval('public.work_type_model_route_id_seq'::regclass);


--
-- Name: api_key_auto_profile api_key_auto_profile_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_key_auto_profile
    ADD CONSTRAINT api_key_auto_profile_pkey PRIMARY KEY (api_key_id);


--
-- Name: api_key_model_cost api_key_model_cost_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_key_model_cost
    ADD CONSTRAINT api_key_model_cost_pkey PRIMARY KEY (bucket, api_key_id, raw_model);


--
-- Name: api_keys api_keys_key_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_key_hash_key UNIQUE (key_hash);


--
-- Name: api_keys api_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.api_keys
    ADD CONSTRAINT api_keys_pkey PRIMARY KEY (id);


--
-- Name: applications applications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_pkey PRIMARY KEY (id);


--
-- Name: applications applications_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.applications
    ADD CONSTRAINT applications_tenant_id_code_key UNIQUE (tenant_id, code);


--
-- Name: auto_tune_audit auto_tune_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.auto_tune_audit
    ADD CONSTRAINT auto_tune_audit_pkey PRIMARY KEY (id);


--
-- Name: background_tasks background_tasks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.background_tasks
    ADD CONSTRAINT background_tasks_pkey PRIMARY KEY (id);


--
-- Name: billing_orders billing_orders_order_no_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.billing_orders
    ADD CONSTRAINT billing_orders_order_no_key UNIQUE (order_no);


--
-- Name: billing_orders billing_orders_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.billing_orders
    ADD CONSTRAINT billing_orders_pkey PRIMARY KEY (id);


--
-- Name: candidate_failure_logs candidate_failure_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.candidate_failure_logs
    ADD CONSTRAINT candidate_failure_logs_pkey PRIMARY KEY (id);


--
-- Name: credential_model_bindings cmb_unique_credential_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings
    ADD CONSTRAINT cmb_unique_credential_model UNIQUE (credential_id, provider_model_id);


--
-- Name: credential_capabilities credential_capabilities_credential_id_capability_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_capabilities
    ADD CONSTRAINT credential_capabilities_credential_id_capability_key UNIQUE (credential_id, capability);


--
-- Name: credential_capabilities credential_capabilities_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_capabilities
    ADD CONSTRAINT credential_capabilities_pkey PRIMARY KEY (id);


--
-- Name: credential_health_checks credential_health_checks_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_health_checks
    ADD CONSTRAINT credential_health_checks_pkey PRIMARY KEY (id);


--
-- Name: credential_model_bindings credential_model_bindings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_bindings
    ADD CONSTRAINT credential_model_bindings_pkey PRIMARY KEY (id);


--
-- Name: credential_model_call_history credential_model_call_history_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_call_history
    ADD CONSTRAINT credential_model_call_history_pkey PRIMARY KEY (credential_id, raw_model, window_start);


--
-- Name: credential_model_index credential_model_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_index
    ADD CONSTRAINT credential_model_index_pkey PRIMARY KEY (bucket, credential_id, raw_model);


--
-- Name: credential_model_peak_1m credential_model_peak_1m_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_peak_1m
    ADD CONSTRAINT credential_model_peak_1m_pkey PRIMARY KEY (bucket, credential_id, raw_model);


--
-- Name: credential_model_stats_1m credential_model_stats_1m_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_stats_1m
    ADD CONSTRAINT credential_model_stats_1m_pkey PRIMARY KEY (bucket, credential_id, raw_model);


--
-- Name: credential_model_weekly_peak credential_model_weekly_peak_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_model_weekly_peak
    ADD CONSTRAINT credential_model_weekly_peak_pkey PRIMARY KEY (week_start, credential_id, raw_model);


--
-- Name: credential_probe_model_log credential_probe_model_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_probe_model_log
    ADD CONSTRAINT credential_probe_model_log_pkey PRIMARY KEY (id);


--
-- Name: credential_quota_usage credential_quota_usage_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage
    ADD CONSTRAINT credential_quota_usage_pkey PRIMARY KEY (id);


--
-- Name: credential_quota_usage credential_quota_usage_quota_id_window_started_at_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quota_usage
    ADD CONSTRAINT credential_quota_usage_quota_id_window_started_at_key UNIQUE (quota_id, window_started_at);


--
-- Name: credential_quotas credential_quotas_credential_id_quota_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quotas
    ADD CONSTRAINT credential_quotas_credential_id_quota_name_key UNIQUE (credential_id, quota_name);


--
-- Name: credential_quotas credential_quotas_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credential_quotas
    ADD CONSTRAINT credential_quotas_pkey PRIMARY KEY (id);


--
-- Name: credentials credentials_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_pkey PRIMARY KEY (id);


--
-- Name: credentials credentials_unique_provider_label; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credentials
    ADD CONSTRAINT credentials_unique_provider_label UNIQUE (provider_id, tenant_id, label);


--
-- Name: credit_ledger credit_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.credit_ledger
    ADD CONSTRAINT credit_ledger_pkey PRIMARY KEY (id);


--
-- Name: internal_service_keys internal_service_keys_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.internal_service_keys
    ADD CONSTRAINT internal_service_keys_pkey PRIMARY KEY (service_id);


--
-- Name: key_applications key_applications_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.key_applications
    ADD CONSTRAINT key_applications_pkey PRIMARY KEY (id);


--
-- Name: key_rpm_daily key_rpm_daily_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.key_rpm_daily
    ADD CONSTRAINT key_rpm_daily_pkey PRIMARY KEY (api_key_id, day_bucket);


--
-- Name: local_models local_models_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models
    ADD CONSTRAINT local_models_pkey PRIMARY KEY (id);


--
-- Name: local_models local_models_runtime_id_raw_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_models
    ADD CONSTRAINT local_models_runtime_id_raw_name_key UNIQUE (runtime_id, raw_name);


--
-- Name: local_runtimes local_runtimes_host_code_runtime_type_base_url_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes
    ADD CONSTRAINT local_runtimes_host_code_runtime_type_base_url_key UNIQUE (host_code, runtime_type, base_url);


--
-- Name: local_runtimes local_runtimes_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.local_runtimes
    ADD CONSTRAINT local_runtimes_pkey PRIMARY KEY (id);


--
-- Name: maas_settings maas_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.maas_settings
    ADD CONSTRAINT maas_settings_pkey PRIMARY KEY (id);


--
-- Name: model_aliases model_aliases_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_aliases
    ADD CONSTRAINT model_aliases_pkey PRIMARY KEY (id);


--
-- Name: model_credit_rates model_credit_rates_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_credit_rates
    ADD CONSTRAINT model_credit_rates_pkey PRIMARY KEY (canonical_id);


--
-- Name: model_discovery_runs model_discovery_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_discovery_runs
    ADD CONSTRAINT model_discovery_runs_pkey PRIMARY KEY (id);


--
-- Name: model_families model_families_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_families
    ADD CONSTRAINT model_families_pkey PRIMARY KEY (id);


--
-- Name: model_fingerprints model_fingerprints_credential_id_canonical_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_fingerprints
    ADD CONSTRAINT model_fingerprints_credential_id_canonical_id_key UNIQUE (credential_id, canonical_id);


--
-- Name: model_fingerprints model_fingerprints_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_fingerprints
    ADD CONSTRAINT model_fingerprints_pkey PRIMARY KEY (id);


--
-- Name: model_lifecycle_jobs model_lifecycle_jobs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_lifecycle_jobs
    ADD CONSTRAINT model_lifecycle_jobs_pkey PRIMARY KEY (id);


--
-- Name: model_offer_events model_offer_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offer_events
    ADD CONSTRAINT model_offer_events_pkey PRIMARY KEY (id);


--
-- Name: model_offers_legacy model_offers_credential_id_raw_model_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy
    ADD CONSTRAINT model_offers_credential_id_raw_model_name_key UNIQUE (credential_id, raw_model_name);


--
-- Name: model_offers_legacy model_offers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_offers_legacy
    ADD CONSTRAINT model_offers_pkey PRIMARY KEY (id);


--
-- Name: model_probe_runs model_probe_runs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_runs
    ADD CONSTRAINT model_probe_runs_pkey PRIMARY KEY (id);


--
-- Name: model_probe_state model_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_probe_state
    ADD CONSTRAINT model_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name);


--
-- Name: model_reconcile_log model_reconcile_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_reconcile_log
    ADD CONSTRAINT model_reconcile_log_pkey PRIMARY KEY (id);


--
-- Name: model_task_index model_task_index_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.model_task_index
    ADD CONSTRAINT model_task_index_pkey PRIMARY KEY (bucket, canonical_id, task_type);


--
-- Name: models_canonical models_canonical_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical
    ADD CONSTRAINT models_canonical_canonical_name_key UNIQUE (canonical_name);


--
-- Name: models_canonical models_canonical_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.models_canonical
    ADD CONSTRAINT models_canonical_pkey PRIMARY KEY (id);


--
-- Name: ops_model_offers_backup ops_model_offers_backup_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.ops_model_offers_backup
    ADD CONSTRAINT ops_model_offers_backup_pkey PRIMARY KEY (backup_id);


--
-- Name: passive_probe_state passive_probe_state_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.passive_probe_state
    ADD CONSTRAINT passive_probe_state_pkey PRIMARY KEY (credential_id, raw_model_name, error_kind);


--
-- Name: price_change_events price_change_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.price_change_events
    ADD CONSTRAINT price_change_events_pkey PRIMARY KEY (id);


--
-- Name: pricing_plans pricing_plans_no_overlap; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_plans
    ADD CONSTRAINT pricing_plans_no_overlap EXCLUDE USING gist (offer_scope_key WITH =, tstzrange(effective_from, COALESCE(effective_to, 'infinity'::timestamp with time zone), '[)'::text) WITH &&);


--
-- Name: pricing_plans pricing_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_plans
    ADD CONSTRAINT pricing_plans_pkey PRIMARY KEY (id);


--
-- Name: pricing_refresh_log pricing_refresh_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.pricing_refresh_log
    ADD CONSTRAINT pricing_refresh_log_pkey PRIMARY KEY (id);


--
-- Name: provider_catalog provider_catalog_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_catalog
    ADD CONSTRAINT provider_catalog_pkey PRIMARY KEY (code);


--
-- Name: provider_events provider_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_events
    ADD CONSTRAINT provider_events_pkey PRIMARY KEY (id);


--
-- Name: provider_header_profiles provider_header_profiles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles
    ADD CONSTRAINT provider_header_profiles_pkey PRIMARY KEY (id);


--
-- Name: provider_header_profiles provider_header_profiles_profile_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_header_profiles
    ADD CONSTRAINT provider_header_profiles_profile_code_key UNIQUE (profile_code);


--
-- Name: provider_models provider_models_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models
    ADD CONSTRAINT provider_models_pkey PRIMARY KEY (id);


--
-- Name: provider_models provider_models_unique_provider_model; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_models
    ADD CONSTRAINT provider_models_unique_provider_model UNIQUE (provider_id, raw_model_name);


--
-- Name: provider_quality_rollup provider_quality_rollup_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_quality_rollup
    ADD CONSTRAINT provider_quality_rollup_pkey PRIMARY KEY (provider_id, bucket_start);


--
-- Name: provider_scores provider_scores_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_scores
    ADD CONSTRAINT provider_scores_pkey PRIMARY KEY (id);


--
-- Name: provider_settings provider_settings_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings
    ADD CONSTRAINT provider_settings_pkey PRIMARY KEY (id);


--
-- Name: provider_settings provider_settings_unique_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.provider_settings
    ADD CONSTRAINT provider_settings_unique_key UNIQUE (provider_id, setting_key);


--
-- Name: providers providers_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_pkey PRIMARY KEY (id);


--
-- Name: providers providers_tenant_id_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.providers
    ADD CONSTRAINT providers_tenant_id_code_key UNIQUE (tenant_id, code);


--
-- Name: request_envelope request_envelope_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_envelope
    ADD CONSTRAINT request_envelope_pkey PRIMARY KEY (request_id);


--
-- Name: request_logs request_logs_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs
    ADD CONSTRAINT request_logs_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_2026_04 request_logs_2026_04_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_2026_04
    ADD CONSTRAINT request_logs_2026_04_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_2026_05 request_logs_2026_05_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_2026_05
    ADD CONSTRAINT request_logs_2026_05_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_2026_06 request_logs_2026_06_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_2026_06
    ADD CONSTRAINT request_logs_2026_06_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_2026_07 request_logs_2026_07_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_2026_07
    ADD CONSTRAINT request_logs_2026_07_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_2026_08 request_logs_2026_08_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_2026_08
    ADD CONSTRAINT request_logs_2026_08_pkey PRIMARY KEY (id, ts);


--
-- Name: request_logs_default request_logs_default_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_logs_default
    ADD CONSTRAINT request_logs_default_pkey PRIMARY KEY (id, ts);


--
-- Name: request_wal request_wal_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal
    ADD CONSTRAINT request_wal_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: request_wal_2026_06 request_wal_2026_06_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal_2026_06
    ADD CONSTRAINT request_wal_2026_06_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: request_wal_2026_07 request_wal_2026_07_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal_2026_07
    ADD CONSTRAINT request_wal_2026_07_pkey PRIMARY KEY (request_id, created_at);


--
-- Name: request_wal_bodies request_wal_bodies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.request_wal_bodies
    ADD CONSTRAINT request_wal_bodies_pkey PRIMARY KEY (request_id);


--
-- Name: route_decisions route_decisions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.route_decisions
    ADD CONSTRAINT route_decisions_pkey PRIMARY KEY (id);


--
-- Name: routing_audit_log routing_audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_audit_log
    ADD CONSTRAINT routing_audit_log_pkey PRIMARY KEY (id);


--
-- Name: routing_decision_log routing_decision_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_decision_log
    ADD CONSTRAINT routing_decision_log_pkey PRIMARY KEY (ts, request_id);


--
-- Name: routing_overrides_audit routing_overrides_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides_audit
    ADD CONSTRAINT routing_overrides_audit_pkey PRIMARY KEY (id);


--
-- Name: routing_overrides routing_overrides_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_overrides
    ADD CONSTRAINT routing_overrides_pkey PRIMARY KEY (id);


--
-- Name: routing_policy routing_policy_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.routing_policy
    ADD CONSTRAINT routing_policy_pkey PRIMARY KEY (id);


--
-- Name: schema_migration_audit schema_migration_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migration_audit
    ADD CONSTRAINT schema_migration_audit_pkey PRIMARY KEY (migration_id);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: security_audit_log security_audit_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.security_audit_log
    ADD CONSTRAINT security_audit_log_pkey PRIMARY KEY (id, ts);


--
-- Name: session_memora_extraction_log session_memora_extraction_log_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_memora_extraction_log
    ADD CONSTRAINT session_memora_extraction_log_pkey PRIMARY KEY (task_id);


--
-- Name: session_titles session_titles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_titles
    ADD CONSTRAINT session_titles_pkey PRIMARY KEY (task_id, scoped_session_id);


--
-- Name: settings_audit settings_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_audit
    ADD CONSTRAINT settings_audit_pkey PRIMARY KEY (id);


--
-- Name: settings_kv settings_kv_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.settings_kv
    ADD CONSTRAINT settings_kv_pkey PRIMARY KEY (key);


--
-- Name: sticky_sessions sticky_sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sticky_sessions
    ADD CONSTRAINT sticky_sessions_pkey PRIMARY KEY (sticky_key);


--
-- Name: subscription_plans subscription_plans_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_plans
    ADD CONSTRAINT subscription_plans_code_key UNIQUE (code);


--
-- Name: subscription_plans subscription_plans_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.subscription_plans
    ADD CONSTRAINT subscription_plans_pkey PRIMARY KEY (id);


--
-- Name: system_identity_pool system_identity_pool_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.system_identity_pool
    ADD CONSTRAINT system_identity_pool_pkey PRIMARY KEY (id);


--
-- Name: tenant_credit_wallets tenant_credit_wallets_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_credit_wallets
    ADD CONSTRAINT tenant_credit_wallets_pkey PRIMARY KEY (tenant_id);


--
-- Name: tenant_model_policies_audit tenant_model_policies_audit_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies_audit
    ADD CONSTRAINT tenant_model_policies_audit_pkey PRIMARY KEY (id);


--
-- Name: tenant_model_policies tenant_model_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies
    ADD CONSTRAINT tenant_model_policies_pkey PRIMARY KEY (id);


--
-- Name: tenant_model_policies tenant_model_policies_tenant_id_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_model_policies
    ADD CONSTRAINT tenant_model_policies_tenant_id_canonical_name_key UNIQUE (tenant_id, canonical_name);


--
-- Name: tenant_settings_kv tenant_settings_kv_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_settings_kv
    ADD CONSTRAINT tenant_settings_kv_pkey PRIMARY KEY (tenant_id, key);


--
-- Name: tenant_subscriptions tenant_subscriptions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_subscriptions
    ADD CONSTRAINT tenant_subscriptions_pkey PRIMARY KEY (id);


--
-- Name: tenant_tool_policies tenant_tool_policies_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_tool_policies
    ADD CONSTRAINT tenant_tool_policies_pkey PRIMARY KEY (id);


--
-- Name: tenants tenants_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenants
    ADD CONSTRAINT tenants_pkey PRIMARY KEY (code);


--
-- Name: token_audit_events token_audit_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.token_audit_events
    ADD CONSTRAINT token_audit_events_pkey PRIMARY KEY (id, ts);


--
-- Name: tool_call_events tool_call_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_call_events
    ADD CONSTRAINT tool_call_events_pkey PRIMARY KEY (id);


--
-- Name: tool_categories tool_categories_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_categories
    ADD CONSTRAINT tool_categories_pkey PRIMARY KEY (id);


--
-- Name: tool_registry tool_registry_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry
    ADD CONSTRAINT tool_registry_pkey PRIMARY KEY (id);


--
-- Name: tool_registry tool_registry_tool_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_registry
    ADD CONSTRAINT tool_registry_tool_name_key UNIQUE (tool_name);


--
-- Name: tool_usage_stats tool_usage_stats_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT tool_usage_stats_pkey PRIMARY KEY (id);


--
-- Name: topup_packages topup_packages_code_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages
    ADD CONSTRAINT topup_packages_code_key UNIQUE (code);


--
-- Name: topup_packages topup_packages_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.topup_packages
    ADD CONSTRAINT topup_packages_pkey PRIMARY KEY (id);


--
-- Name: tuning_params tuning_params_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_params
    ADD CONSTRAINT tuning_params_pkey PRIMARY KEY (key);


--
-- Name: tuning_proposals tuning_proposals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_proposals
    ADD CONSTRAINT tuning_proposals_pkey PRIMARY KEY (id);


--
-- Name: tuning_signals tuning_signals_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tuning_signals
    ADD CONSTRAINT tuning_signals_pkey PRIMARY KEY (id);


--
-- Name: tenant_tool_policies uk_tenant_tool_policy; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tenant_tool_policies
    ADD CONSTRAINT uk_tenant_tool_policy UNIQUE (tenant_id, tool_pattern);


--
-- Name: tool_usage_stats uk_tool_usage_stats; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.tool_usage_stats
    ADD CONSTRAINT uk_tool_usage_stats UNIQUE (tool_id, tenant_id, usage_date);


--
-- Name: usage_ledger usage_ledger_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.usage_ledger
    ADD CONSTRAINT usage_ledger_pkey PRIMARY KEY (id, ts);


--
-- Name: users users_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);


--
-- Name: users users_username_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_username_key UNIQUE (username);


--
-- Name: work_type_config work_type_config_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_config
    ADD CONSTRAINT work_type_config_pkey PRIMARY KEY (key);


--
-- Name: work_type_model_route work_type_model_route_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route
    ADD CONSTRAINT work_type_model_route_pkey PRIMARY KEY (id);


--
-- Name: work_type_model_route work_type_model_route_work_type_key_canonical_name_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.work_type_model_route
    ADD CONSTRAINT work_type_model_route_work_type_key_canonical_name_key UNIQUE (work_type_key, canonical_name);


--
-- Name: credential_model_peak_1m_bucket_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credential_model_peak_1m_bucket_idx ON public.credential_model_peak_1m USING btree (bucket DESC);


--
-- Name: credential_model_stats_1m_bucket_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credential_model_stats_1m_bucket_idx ON public.credential_model_stats_1m USING btree (bucket DESC);


--
-- Name: idx_akmc_api_key_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_api_key_bucket ON public.api_key_model_cost USING btree (api_key_id, bucket DESC);


--
-- Name: idx_akmc_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_canonical ON public.api_key_model_cost USING btree (canonical_id, bucket DESC);


--
-- Name: idx_akmc_pressure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_akmc_pressure ON public.api_key_model_cost USING btree (api_key_id, bucket DESC, pressure_ratio DESC);


--
-- Name: idx_api_keys_application; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_application ON public.api_keys USING btree (application_id);


--
-- Name: idx_api_keys_is_system; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_is_system ON public.api_keys USING btree (is_system) WHERE (is_system = true);


--
-- Name: idx_api_keys_kid; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_kid ON public.api_keys USING btree (key_ciphertext_kid) WHERE (key_ciphertext_kid IS NOT NULL);


--
-- Name: idx_api_keys_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_status ON public.api_keys USING btree (status);


--
-- Name: idx_api_keys_tenant_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_tenant_enabled ON public.api_keys USING btree (tenant_id, enabled);


--
-- Name: idx_api_keys_throttled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_throttled ON public.api_keys USING btree (throttled_at) WHERE (throttled_at IS NOT NULL);


--
-- Name: idx_api_keys_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_tier ON public.api_keys USING btree (key_tier);


--
-- Name: idx_applications_tenant_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_applications_tenant_code ON public.applications USING btree (tenant_id, code) WHERE (enabled = true);


--
-- Name: idx_auto_tune_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_auto_tune_cred ON public.auto_tune_audit USING btree (credential_id, created_at DESC);


--
-- Name: idx_bg_tasks_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_provider ON public.background_tasks USING btree (provider_id, started_at DESC);


--
-- Name: idx_bg_tasks_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_status ON public.background_tasks USING btree (status) WHERE (status = 'running'::text);


--
-- Name: idx_bg_tasks_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_bg_tasks_type ON public.background_tasks USING btree (task_type, started_at DESC);


--
-- Name: idx_billing_orders_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_billing_orders_status ON public.billing_orders USING btree (status, created_at DESC);


--
-- Name: idx_billing_orders_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_billing_orders_tenant ON public.billing_orders USING btree (tenant_id, created_at DESC);


--
-- Name: idx_call_history_cred_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_cred_time ON public.credential_model_call_history USING btree (credential_id, window_start DESC);


--
-- Name: idx_call_history_model_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_call_history_model_time ON public.credential_model_call_history USING btree (raw_model, window_start DESC);


--
-- Name: idx_candidate_failure_logs_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_cred_ts ON public.candidate_failure_logs USING btree (credential_id, ts DESC);


--
-- Name: idx_candidate_failure_logs_model_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_model_ts ON public.candidate_failure_logs USING btree (raw_model_name, ts DESC);


--
-- Name: idx_candidate_failure_logs_provider_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_provider_ts ON public.candidate_failure_logs USING btree (provider_id, ts DESC);


--
-- Name: idx_candidate_failure_logs_req; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_candidate_failure_logs_req ON public.candidate_failure_logs USING btree (request_id);


--
-- Name: idx_cmb_available_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_available_tier ON public.credential_model_bindings USING btree (routing_tier, weight DESC, success_rate DESC NULLS LAST);


--
-- Name: idx_cmb_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_credential ON public.credential_model_bindings USING btree (credential_id);


--
-- Name: idx_cmb_unavailable_recover_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_unavailable_recover_at ON public.credential_model_bindings USING btree (unavailable_recover_at) WHERE (available = false);


--
-- Name: idx_cmi_pressure; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmi_pressure ON public.credential_model_index USING btree (bucket, pressure_ratio DESC);


--
-- Name: idx_cmi_score; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmi_score ON public.credential_model_index USING btree (canonical_id, score_smart DESC, bucket DESC);


--
-- Name: idx_cred_avail_recover; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cred_avail_recover ON public.credentials USING btree (availability_recover_at) WHERE ((availability_state = ANY (ARRAY['cooling'::text, 'rate_limited'::text, 'unreachable'::text])) AND (availability_recover_at IS NOT NULL));


--
-- Name: idx_cred_quota_recover; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cred_quota_recover ON public.credentials USING btree (quota_recover_at) WHERE ((quota_state = 'periodic_exhausted'::text) AND (quota_recover_at IS NOT NULL));


--
-- Name: idx_credential_health_checks_credential_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_credential_created ON public.credential_health_checks USING btree (credential_id, created_at DESC);


--
-- Name: idx_credential_health_checks_provider_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_provider_created ON public.credential_health_checks USING btree (provider_id, created_at DESC);


--
-- Name: idx_credential_health_checks_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_health_checks_run ON public.credential_health_checks USING btree (run_id, created_at DESC);


--
-- Name: idx_credential_probe_model_log_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probe_model_log_cred ON public.credential_probe_model_log USING btree (credential_id, created_at DESC);


--
-- Name: idx_credential_probe_model_log_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_probe_model_log_tenant_time ON public.credential_probe_model_log USING btree (tenant_id, created_at DESC);


--
-- Name: idx_credential_quota_usage_quota; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_quota_usage_quota ON public.credential_quota_usage USING btree (quota_id, window_started_at DESC);


--
-- Name: idx_credential_quotas_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_quotas_credential ON public.credential_quotas USING btree (credential_id) WHERE (enabled = true);


--
-- Name: idx_credentials_api_models_checked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_api_models_checked ON public.credentials USING btree (api_models_last_checked_at) WHERE (api_models_last_checked_at IS NOT NULL);


--
-- Name: idx_credentials_auto_limit; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_auto_limit ON public.credentials USING btree (concurrency_limit_auto) WHERE (concurrency_limit_auto IS NOT NULL);


--
-- Name: idx_credentials_default_probe_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_default_probe_model ON public.credentials USING btree (default_probe_model) WHERE (default_probe_model IS NOT NULL);


--
-- Name: idx_credentials_effective_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_effective_at ON public.credentials USING btree (effective_at) WHERE (effective_at IS NOT NULL);


--
-- Name: idx_credentials_expires_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_expires_at ON public.credentials USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_credentials_free_source; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_free_source ON public.credentials USING btree (acquisition_source) WHERE ((pool_group = 'free'::text) AND (acquisition_source IS NOT NULL));


--
-- Name: idx_credentials_health_checked_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_health_checked_at ON public.credentials USING btree (tenant_id, health_checked_at DESC) WHERE (health_checked_at IS NOT NULL);


--
-- Name: idx_credentials_health_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_health_status ON public.credentials USING btree (tenant_id, health_status);


--
-- Name: idx_credentials_manual_disabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_manual_disabled ON public.credentials USING btree (manual_disabled) WHERE (manual_disabled = true);


--
-- Name: idx_credentials_pool_group; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_pool_group ON public.credentials USING btree (pool_group) WHERE (pool_group IS NOT NULL);


--
-- Name: idx_credentials_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_provider ON public.credentials USING btree (provider_id);


--
-- Name: idx_credentials_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_status ON public.credentials USING btree (status);


--
-- Name: idx_credentials_tags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_tags ON public.credentials USING gin (tags) WHERE (tags IS NOT NULL);


--
-- Name: idx_credentials_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_tenant ON public.credentials USING btree (tenant_id);


--
-- Name: idx_credit_ledger_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credit_ledger_tenant_ts ON public.credit_ledger USING btree (tenant_id, created_at DESC);


--
-- Name: idx_envelope_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_envelope_credential ON public.request_envelope USING btree (credential_id);


--
-- Name: idx_envelope_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_envelope_expires ON public.request_envelope USING btree (expires_at);


--
-- Name: idx_internal_service_keys_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_internal_service_keys_enabled ON public.internal_service_keys USING btree (enabled);


--
-- Name: idx_key_applications_client_ip; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_client_ip ON public.key_applications USING btree (client_ip, created_at DESC);


--
-- Name: idx_key_applications_fingerprint; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_fingerprint ON public.key_applications USING btree (fingerprint, status);


--
-- Name: idx_key_applications_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_status_created ON public.key_applications USING btree (status, created_at DESC);


--
-- Name: idx_key_rpm_daily_key_day; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_rpm_daily_key_day ON public.key_rpm_daily USING btree (api_key_id, day_bucket DESC);


--
-- Name: idx_local_models_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_local_models_canonical ON public.local_models USING btree (canonical_id) WHERE (canonical_id IS NOT NULL);


--
-- Name: idx_local_runtimes_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_local_runtimes_status ON public.local_runtimes USING btree (status);


--
-- Name: idx_model_aliases_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_aliases_status ON public.model_aliases USING btree (status);


--
-- Name: idx_model_discovery_runs_running_lease; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_discovery_runs_running_lease ON public.model_discovery_runs USING btree (tenant_id, lease_expires_at) WHERE (status = 'running'::text);


--
-- Name: idx_model_discovery_runs_tenant_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_discovery_runs_tenant_started ON public.model_discovery_runs USING btree (tenant_id, started_at DESC);


--
-- Name: idx_model_fingerprints_drift; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_fingerprints_drift ON public.model_fingerprints USING btree (drift_detected) WHERE (drift_detected = true);


--
-- Name: idx_model_offer_events_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_cred_ts ON public.model_offer_events USING btree (credential_id, ts DESC);


--
-- Name: idx_model_offer_events_raw_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_raw_ts ON public.model_offer_events USING btree (raw_model_name, ts DESC);


--
-- Name: idx_model_offer_events_run; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offer_events_run ON public.model_offer_events USING btree (run_id) WHERE (run_id IS NOT NULL);


--
-- Name: idx_model_offers_available; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_available ON public.model_offers_legacy USING btree (available) WHERE (available = true);


--
-- Name: idx_model_offers_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_canonical ON public.model_offers_legacy USING btree (canonical_id);


--
-- Name: idx_model_offers_manual_priority; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_manual_priority ON public.model_offers_legacy USING btree (manual_priority);


--
-- Name: idx_model_offers_standardized_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_offers_standardized_name ON public.model_offers_legacy USING btree (standardized_name) WHERE (standardized_name IS NOT NULL);


--
-- Name: idx_model_probe_state_retry; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_model_probe_state_retry ON public.model_probe_state USING btree (state, next_retry_at) WHERE (state = 'recovering'::text);


--
-- Name: idx_models_canonical_family_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_family_status ON public.models_canonical USING btree (family, status);


--
-- Name: idx_models_canonical_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_status ON public.models_canonical USING btree (status);


--
-- Name: idx_models_canonical_tags_gin; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_tags_gin ON public.models_canonical USING gin (tags);


--
-- Name: idx_models_canonical_tags_locked; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_models_canonical_tags_locked ON public.models_canonical USING btree (tags_locked) WHERE (tags_locked = true);


--
-- Name: idx_mpr_cred_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_cred_created ON public.model_probe_runs USING btree (credential_id, created_at DESC);


--
-- Name: idx_mpr_model_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_model_created ON public.model_probe_runs USING btree (raw_model_name, created_at DESC);


--
-- Name: idx_mpr_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_status_created ON public.model_probe_runs USING btree (status, created_at DESC) WHERE (status <> 'ok'::text);


--
-- Name: idx_mps_due; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mps_due ON public.model_probe_state USING btree (next_retry_at) WHERE (state = ANY (ARRAY['unknown'::text, 'recovering'::text]));


--
-- Name: idx_mti_canonical_bkt; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mti_canonical_bkt ON public.model_task_index USING btree (canonical_id, bucket DESC);


--
-- Name: idx_mti_task_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mti_task_bucket ON public.model_task_index USING btree (task_type, bucket DESC);


--
-- Name: idx_passive_probe_reviewing; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_passive_probe_reviewing ON public.passive_probe_state USING btree (in_reviewing, reviewing_until) WHERE (in_reviewing = true);


--
-- Name: idx_peak_1m_cred_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_peak_1m_cred_bucket ON public.credential_model_peak_1m USING btree (credential_id, bucket DESC);


--
-- Name: idx_peak_1m_model_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_peak_1m_model_bucket ON public.credential_model_peak_1m USING btree (raw_model, bucket DESC);


--
-- Name: idx_pricing_plans_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_credential ON public.pricing_plans USING btree (credential_id) WHERE (credential_id IS NOT NULL);


--
-- Name: idx_pricing_plans_effective_to_null; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_effective_to_null ON public.pricing_plans USING btree (effective_to) WHERE (effective_to IS NULL);


--
-- Name: idx_pricing_plans_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_model ON public.pricing_plans USING btree (model_canonical_id) WHERE (model_canonical_id IS NOT NULL);


--
-- Name: idx_pricing_plans_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_provider ON public.pricing_plans USING btree (provider_id) WHERE (provider_id IS NOT NULL);


--
-- Name: idx_pricing_plans_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_plans_scope ON public.pricing_plans USING btree (scope);


--
-- Name: idx_pricing_refresh_log_run_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_refresh_log_run_ts ON public.pricing_refresh_log USING btree (run_ts DESC);


--
-- Name: idx_pricing_refresh_log_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pricing_refresh_log_status ON public.pricing_refresh_log USING btree (status);


--
-- Name: idx_provider_events_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_events_credential_ts ON public.provider_events USING btree (credential_id, ts DESC);


--
-- Name: idx_provider_models_available; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_available ON public.provider_models USING btree (available) WHERE (available = true);


--
-- Name: idx_provider_models_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_canonical ON public.provider_models USING btree (canonical_id);


--
-- Name: idx_provider_models_standardized; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_models_standardized ON public.provider_models USING btree (standardized_name) WHERE (standardized_name IS NOT NULL);


--
-- Name: idx_provider_quality_rollup_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_quality_rollup_bucket ON public.provider_quality_rollup USING btree (bucket_start DESC);


--
-- Name: idx_provider_settings_key; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_settings_key ON public.provider_settings USING btree (setting_key) WHERE (enabled = true);


--
-- Name: idx_provider_settings_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_provider_settings_provider ON public.provider_settings USING btree (provider_id) WHERE (enabled = true);


--
-- Name: idx_providers_catalog_code; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_catalog_code ON public.providers USING btree (catalog_code);


--
-- Name: idx_providers_catalog_vendor; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_catalog_vendor ON public.providers USING btree (catalog_code) WHERE (catalog_code IS NOT NULL);


--
-- Name: idx_providers_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_category ON public.providers USING btree (category);


--
-- Name: idx_providers_manual_disabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_manual_disabled ON public.providers USING btree (manual_disabled) WHERE (manual_disabled = true);


--
-- Name: idx_providers_tenant_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_tenant_enabled ON public.providers USING btree (tenant_id, enabled);


--
-- Name: idx_request_logs_api_key_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_api_key_ts ON ONLY public.request_logs USING btree (api_key_id, ts DESC);


--
-- Name: idx_request_logs_auto; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_auto ON ONLY public.request_logs USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: idx_request_logs_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_credential_ts ON ONLY public.request_logs USING btree (credential_id, ts DESC);


--
-- Name: idx_request_logs_credits_charged; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_credits_charged ON ONLY public.request_logs USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: idx_request_logs_explicit_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_explicit_model ON ONLY public.request_logs USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: INDEX idx_request_logs_explicit_model; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON INDEX public.idx_request_logs_explicit_model IS 'Supports the routing-v2 explicit-model analytics path (handleMatrix/handleFlow/handleAudit) where client_model is used in place of outbound_model.';


--
-- Name: idx_request_logs_failure_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_failure_ts ON ONLY public.request_logs USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: idx_request_logs_gw_session_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_gw_session_ts ON ONLY public.request_logs USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: idx_request_logs_gw_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_gw_task_ts ON ONLY public.request_logs USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: idx_request_logs_identity_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_identity_hash ON ONLY public.request_logs USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: idx_request_logs_identity_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_identity_ts ON ONLY public.request_logs USING btree (identity_hash, ts DESC);


--
-- Name: idx_request_logs_is_auto_request_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_is_auto_request_task_ts ON ONLY public.request_logs USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: idx_request_logs_model_chosen_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_model_chosen_ts ON ONLY public.request_logs USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: idx_request_logs_outbound_msg_count; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_outbound_msg_count ON ONLY public.request_logs USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: idx_request_logs_owner_user_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_owner_user_ts ON ONLY public.request_logs USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: idx_request_logs_parent_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_parent_ts ON ONLY public.request_logs USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: idx_request_logs_provider_quality; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_quality ON ONLY public.request_logs USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: idx_request_logs_provider_tool_calls; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_tool_calls ON ONLY public.request_logs USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: idx_request_logs_provider_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_provider_ts ON ONLY public.request_logs USING btree (provider_id, ts DESC);


--
-- Name: idx_request_logs_quality_flags; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_quality_flags ON ONLY public.request_logs USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: idx_request_logs_request_id_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_request_id_ts ON ONLY public.request_logs USING btree (request_id, ts DESC);


--
-- Name: idx_request_logs_request_id_ts_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_request_logs_request_id_ts_unique ON ONLY public.request_logs USING btree (request_id, ts);


--
-- Name: idx_request_logs_search_trgm_2026_04; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_04 ON public.request_logs_2026_04 USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_search_trgm_2026_05; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_05 ON public.request_logs_2026_05 USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_search_trgm_2026_06; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_06 ON public.request_logs_2026_06 USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_search_trgm_2026_07; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_07 ON public.request_logs_2026_07 USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_search_trgm_2026_08; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_2026_08 ON public.request_logs_2026_08 USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_search_trgm_default; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_search_trgm_default ON public.request_logs_default USING gin (search_text public.gin_trgm_ops);


--
-- Name: idx_request_logs_session_outbound; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_session_outbound ON ONLY public.request_logs USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: idx_request_logs_status_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_status_ts ON ONLY public.request_logs USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: idx_request_logs_stream_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_stream_ts ON ONLY public.request_logs USING btree (stream_interrupted, ts DESC);


--
-- Name: idx_request_logs_tenant_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_tenant_task_ts ON ONLY public.request_logs USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: idx_request_logs_tool_calls; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_tool_calls ON ONLY public.request_logs USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: idx_request_logs_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_ts ON ONLY public.request_logs USING btree (ts DESC);


--
-- Name: idx_request_logs_upstream_finish_reason; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_upstream_finish_reason ON ONLY public.request_logs USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: idx_request_logs_usage_source_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_usage_source_ts ON ONLY public.request_logs USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: idx_request_logs_work_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_work_type ON ONLY public.request_logs USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: idx_route_decisions_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_route_decisions_ts ON public.route_decisions USING btree (ts DESC);


--
-- Name: idx_routing_audit_log_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_audit_log_ts ON public.routing_audit_log USING btree (ts DESC);


--
-- Name: idx_routing_decision_log_canonical_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_canonical_ts ON public.routing_decision_log USING btree (canonical_model, ts DESC);


--
-- Name: idx_routing_decision_log_cred_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_cred_ts ON public.routing_decision_log USING btree (chosen_credential_id, ts DESC);


--
-- Name: idx_routing_decision_log_identity_hash; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_identity_hash ON public.routing_decision_log USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: idx_routing_decision_log_model_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_model_ts ON public.routing_decision_log USING btree (model, ts DESC);


--
-- Name: idx_routing_decision_log_request_id_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_decision_log_request_id_ts ON public.routing_decision_log USING btree (request_id, ts DESC);


--
-- Name: idx_routing_overrides_audit_actor_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_actor_ts ON public.routing_overrides_audit USING btree (actor, ts DESC) WHERE (actor IS NOT NULL);


--
-- Name: idx_routing_overrides_audit_override_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_override_ts ON public.routing_overrides_audit USING btree (override_id, ts DESC) WHERE (override_id IS NOT NULL);


--
-- Name: idx_routing_overrides_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_audit_ts ON public.routing_overrides_audit USING btree (ts DESC);


--
-- Name: idx_routing_overrides_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_expires ON public.routing_overrides USING btree (expires_at) WHERE (expires_at IS NOT NULL);


--
-- Name: idx_routing_overrides_task_profile; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_routing_overrides_task_profile ON public.routing_overrides USING btree (task_type, profile);


--
-- Name: idx_routing_overrides_unique; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_routing_overrides_unique ON public.routing_overrides USING btree (task_type, profile, COALESCE(model_chosen, ''::text), mode);


--
-- Name: idx_security_audit_log_api_key_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_api_key_ts ON public.security_audit_log USING btree (api_key_id, ts DESC) WHERE (api_key_id IS NOT NULL);


--
-- Name: idx_security_audit_log_event_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_event_kind ON public.security_audit_log USING btree (event_kind, ts DESC);


--
-- Name: idx_security_audit_log_internal_svc; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_internal_svc ON public.security_audit_log USING btree (internal_service_id, ts DESC) WHERE (internal_service_id IS NOT NULL);


--
-- Name: idx_security_audit_log_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_security_audit_log_ts ON public.security_audit_log USING btree (ts DESC);


--
-- Name: idx_session_memora_extraction_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_memora_extraction_at ON public.session_memora_extraction_log USING btree (extracted_at DESC);


--
-- Name: idx_session_titles_generated_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_titles_generated_at ON public.session_titles USING btree (generated_at DESC);


--
-- Name: idx_settings_audit_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_created ON public.settings_audit USING btree (created_at);


--
-- Name: idx_settings_audit_key_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_key_time ON public.settings_audit USING btree (setting_key, created_at DESC);


--
-- Name: idx_settings_audit_operator; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_operator ON public.settings_audit USING btree (operator_user, created_at DESC);


--
-- Name: idx_settings_audit_tenant_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_audit_tenant_time ON public.settings_audit USING btree (tenant_id, created_at DESC);


--
-- Name: idx_settings_kv_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_category ON public.settings_kv USING btree (category);


--
-- Name: idx_settings_kv_scope; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_scope ON public.settings_kv USING btree (scope);


--
-- Name: idx_settings_kv_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_settings_kv_updated ON public.settings_kv USING btree (updated_at DESC);


--
-- Name: idx_stats_1m_cred_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stats_1m_cred_bucket ON public.credential_model_stats_1m USING btree (credential_id, bucket DESC);


--
-- Name: idx_stats_1m_model_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stats_1m_model_bucket ON public.credential_model_stats_1m USING btree (raw_model, bucket DESC);


--
-- Name: idx_sticky_sessions_expires; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sticky_sessions_expires ON public.sticky_sessions USING btree (expires_at);


--
-- Name: idx_tenant_settings_kv_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_settings_kv_category ON public.tenant_settings_kv USING btree (category);


--
-- Name: idx_tenant_settings_kv_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_settings_kv_tenant ON public.tenant_settings_kv USING btree (tenant_id);


--
-- Name: idx_tenant_subscriptions_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_subscriptions_tenant ON public.tenant_subscriptions USING btree (tenant_id, status);


--
-- Name: idx_tenant_tool_policies_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_tool_policies_tenant ON public.tenant_tool_policies USING btree (tenant_id) WHERE (enabled = true);


--
-- Name: idx_tenants_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenants_name ON public.tenants USING btree (name);


--
-- Name: idx_tenants_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenants_status ON public.tenants USING btree (status);


--
-- Name: idx_tmp_audit_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_audit_tenant_ts ON public.tenant_model_policies_audit USING btree (tenant_id, ts DESC);


--
-- Name: idx_tmp_audit_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_audit_ts ON public.tenant_model_policies_audit USING btree (ts DESC);


--
-- Name: idx_tmp_canonical; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_canonical ON public.tenant_model_policies USING btree (canonical_name);


--
-- Name: idx_tmp_tenant_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tmp_tenant_active ON public.tenant_model_policies USING btree (tenant_id) WHERE (deleted_at IS NULL);


--
-- Name: idx_token_audit_events_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_token_audit_events_credential ON public.token_audit_events USING btree (credential_id, ts DESC);


--
-- Name: idx_tool_call_events_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_call_events_tenant_id ON public.tool_call_events USING btree (tenant_id, called_at DESC);


--
-- Name: idx_tool_call_events_tool_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_call_events_tool_id ON public.tool_call_events USING btree (tool_id, called_at DESC);


--
-- Name: idx_tool_categories_order; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_categories_order ON public.tool_categories USING btree (display_order) WHERE (enabled = true);


--
-- Name: idx_tool_registry_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_category ON public.tool_registry USING btree (category) WHERE (enabled = true);


--
-- Name: idx_tool_registry_deprecation; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_deprecation ON public.tool_registry USING btree (deprecation_date) WHERE (deprecation_date IS NOT NULL);


--
-- Name: idx_tool_registry_name; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_name ON public.tool_registry USING btree (tool_name) WHERE (enabled = true);


--
-- Name: idx_tool_registry_tenant_tool; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_tenant_tool ON public.tool_registry USING btree (tenant_id, tool_id, version DESC);


--
-- Name: idx_tool_registry_unique_version; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tool_registry_unique_version ON public.tool_registry USING btree (tenant_id, tool_id, version);


--
-- Name: idx_tool_usage_stats_date; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_date ON public.tool_usage_stats USING btree (usage_date DESC);


--
-- Name: idx_tool_usage_stats_tenant_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tenant_id ON public.tool_usage_stats USING btree (tenant_id);


--
-- Name: idx_tool_usage_stats_tool_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_usage_stats_tool_id ON public.tool_usage_stats USING btree (tool_id);


--
-- Name: idx_tuning_params_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_params_category ON public.tuning_params USING btree (category, enabled) WHERE (enabled = true);


--
-- Name: idx_tuning_proposals_cat; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_cat ON public.tuning_proposals USING btree (category, task_type) WHERE (status = 'pending'::text);


--
-- Name: idx_tuning_proposals_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_created ON public.tuning_proposals USING btree (created_at) WHERE (status = 'pending'::text);


--
-- Name: idx_tuning_proposals_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_proposals_status ON public.tuning_proposals USING btree (status, ts DESC);


--
-- Name: idx_tuning_signals_5m_pk; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tuning_signals_5m_pk ON public.tuning_signals_5m USING btree (bucket, task_type, classifier);


--
-- Name: idx_tuning_signals_5m_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_5m_task_ts ON public.tuning_signals_5m USING btree (task_type, classifier, bucket DESC);


--
-- Name: idx_tuning_signals_daily_pk; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tuning_signals_daily_pk ON public.tuning_signals_daily USING btree (bucket, task_type, classifier);


--
-- Name: idx_tuning_signals_daily_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_daily_task_ts ON public.tuning_signals_daily USING btree (task_type, classifier, bucket DESC);


--
-- Name: idx_tuning_signals_lowq; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_lowq ON public.tuning_signals USING btree (task_type, ts DESC) WHERE ((quality_score < 0.5) AND (classifier = 'heuristic'::text));


--
-- Name: idx_tuning_signals_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_session ON public.tuning_signals USING btree (session_id, ts DESC) WHERE (session_id IS NOT NULL);


--
-- Name: idx_tuning_signals_strategy_task; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_strategy_task ON public.tuning_signals USING btree (strategy, task_type, ts DESC) WHERE (task_type IS NOT NULL);


--
-- Name: idx_tuning_signals_strategy_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_strategy_ts ON public.tuning_signals USING btree (strategy, ts DESC);


--
-- Name: idx_tuning_signals_task_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tuning_signals_task_ts ON public.tuning_signals USING btree (task_type, ts DESC);


--
-- Name: idx_usage_ledger_app_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_app_ts ON public.usage_ledger USING btree (application_id, ts DESC) WHERE (application_id IS NOT NULL);


--
-- Name: idx_usage_ledger_application_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_application_id ON public.usage_ledger USING btree (application_id) WHERE (application_id IS NOT NULL);


--
-- Name: idx_usage_ledger_credential_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_credential_ts ON public.usage_ledger USING btree (credential_id, ts DESC);


--
-- Name: idx_usage_ledger_request_id; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_request_id ON public.usage_ledger USING btree (request_id);


--
-- Name: idx_usage_ledger_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_tenant_ts ON public.usage_ledger USING btree (tenant_id, ts DESC);


--
-- Name: idx_usage_ledger_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_ledger_ts ON public.usage_ledger USING btree (ts DESC);


--
-- Name: idx_usage_minute_canonical_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_canonical_bucket ON public.usage_minute USING btree (canonical_id, bucket DESC);


--
-- Name: idx_usage_minute_credential_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_credential_bucket ON public.usage_minute USING btree (credential_id, bucket DESC);


--
-- Name: idx_usage_minute_tenant_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_tenant_bucket ON public.usage_minute USING btree (tenant_id, bucket DESC);


--
-- Name: idx_users_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_tenant ON public.users USING btree (tenant_id);


--
-- Name: idx_users_username; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_users_username ON public.users USING btree (username);


--
-- Name: idx_wal_session; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_session ON ONLY public.request_wal USING btree (gw_session_id, created_at);


--
-- Name: idx_wal_status_stage; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_status_stage ON ONLY public.request_wal USING btree (status, stage);


--
-- Name: idx_wal_tenant_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wal_tenant_created ON ONLY public.request_wal USING btree (tenant_id, created_at DESC);


--
-- Name: idx_weekly_peak_cred; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_weekly_peak_cred ON public.credential_model_weekly_peak USING btree (credential_id, week_start DESC);


--
-- Name: idx_work_type_config_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_work_type_config_category ON public.work_type_config USING btree (category, sort_order);


--
-- Name: idx_work_type_config_l1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_work_type_config_l1 ON public.work_type_config USING btree (l1_task_type);


--
-- Name: idx_wtmr_work_type; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_wtmr_work_type ON public.work_type_model_route USING btree (work_type_key);


--
-- Name: request_logs_2026_04_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_api_key_id_ts_idx ON public.request_logs_2026_04 USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_2026_04_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_client_model_ts_idx ON public.request_logs_2026_04 USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_2026_04_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_credential_id_ts_idx ON public.request_logs_2026_04 USING btree (credential_id, ts DESC);


--
-- Name: request_logs_2026_04_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_failure_stage_failure_detail_code_ts_idx ON public.request_logs_2026_04 USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_2026_04_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_gw_session_id_ts_idx ON public.request_logs_2026_04 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_04_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_gw_session_id_ts_idx1 ON public.request_logs_2026_04 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_04_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_gw_task_id_ts_idx ON public.request_logs_2026_04 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_04_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_identity_hash_ts_idx ON public.request_logs_2026_04 USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_2026_04_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_identity_hash_ts_idx1 ON public.request_logs_2026_04 USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_2026_04_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_2026_04 USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_2026_04_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_is_auto_request_task_type_ts_idx ON public.request_logs_2026_04 USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_2026_04_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_model_chosen_ts_idx ON public.request_logs_2026_04 USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_2026_04_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_owner_user_ts_idx ON public.request_logs_2026_04 USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_2026_04_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_parent_request_id_ts_idx ON public.request_logs_2026_04 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_04_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_provider_id_quality_score_ts_idx ON public.request_logs_2026_04 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_04_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_provider_id_ts_idx ON public.request_logs_2026_04 USING btree (provider_id, ts DESC);


--
-- Name: request_logs_2026_04_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_provider_id_ts_idx1 ON public.request_logs_2026_04 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_04_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_quality_flags_idx ON public.request_logs_2026_04 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_04_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_request_id_ts_idx ON public.request_logs_2026_04 USING btree (request_id, ts DESC);


--
-- Name: request_logs_2026_04_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_04_request_id_ts_idx1 ON public.request_logs_2026_04 USING btree (request_id, ts);


--
-- Name: request_logs_2026_04_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_request_status_ts_idx ON public.request_logs_2026_04 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_04_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_stream_interrupted_ts_idx ON public.request_logs_2026_04 USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_2026_04_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_04 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_04_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_tenant_id_ts_idx ON public.request_logs_2026_04 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_04_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_tenant_id_ts_idx1 ON public.request_logs_2026_04 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_04_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_tool_calls_idx ON public.request_logs_2026_04 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_04_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_ts_idx ON public.request_logs_2026_04 USING btree (ts DESC);


--
-- Name: request_logs_2026_04_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_upstream_finish_reason_ts_idx ON public.request_logs_2026_04 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_04_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_usage_source_ts_idx ON public.request_logs_2026_04 USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_2026_04_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_04_work_type_ts_idx ON public.request_logs_2026_04 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_05_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_api_key_id_ts_idx ON public.request_logs_2026_05 USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_2026_05_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_client_model_ts_idx ON public.request_logs_2026_05 USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_2026_05_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_credential_id_ts_idx ON public.request_logs_2026_05 USING btree (credential_id, ts DESC);


--
-- Name: request_logs_2026_05_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_failure_stage_failure_detail_code_ts_idx ON public.request_logs_2026_05 USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_2026_05_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_gw_session_id_ts_idx ON public.request_logs_2026_05 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_05_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_gw_session_id_ts_idx1 ON public.request_logs_2026_05 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_05_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_gw_task_id_ts_idx ON public.request_logs_2026_05 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_05_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_identity_hash_ts_idx ON public.request_logs_2026_05 USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_2026_05_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_identity_hash_ts_idx1 ON public.request_logs_2026_05 USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_2026_05_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_2026_05 USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_2026_05_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_is_auto_request_task_type_ts_idx ON public.request_logs_2026_05 USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_2026_05_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_model_chosen_ts_idx ON public.request_logs_2026_05 USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_2026_05_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_owner_user_ts_idx ON public.request_logs_2026_05 USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_2026_05_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_parent_request_id_ts_idx ON public.request_logs_2026_05 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_05_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_provider_id_quality_score_ts_idx ON public.request_logs_2026_05 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_05_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_provider_id_ts_idx ON public.request_logs_2026_05 USING btree (provider_id, ts DESC);


--
-- Name: request_logs_2026_05_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_provider_id_ts_idx1 ON public.request_logs_2026_05 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_05_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_quality_flags_idx ON public.request_logs_2026_05 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_05_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_request_id_ts_idx ON public.request_logs_2026_05 USING btree (request_id, ts DESC);


--
-- Name: request_logs_2026_05_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_05_request_id_ts_idx1 ON public.request_logs_2026_05 USING btree (request_id, ts);


--
-- Name: request_logs_2026_05_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_request_status_ts_idx ON public.request_logs_2026_05 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_05_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_stream_interrupted_ts_idx ON public.request_logs_2026_05 USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_2026_05_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_05 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_05_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_tenant_id_ts_idx ON public.request_logs_2026_05 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_05_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_tenant_id_ts_idx1 ON public.request_logs_2026_05 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_05_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_tool_calls_idx ON public.request_logs_2026_05 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_05_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_ts_idx ON public.request_logs_2026_05 USING btree (ts DESC);


--
-- Name: request_logs_2026_05_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_upstream_finish_reason_ts_idx ON public.request_logs_2026_05 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_05_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_usage_source_ts_idx ON public.request_logs_2026_05 USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_2026_05_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_05_work_type_ts_idx ON public.request_logs_2026_05 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_06_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_api_key_id_ts_idx ON public.request_logs_2026_06 USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_2026_06_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_client_model_ts_idx ON public.request_logs_2026_06 USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_2026_06_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_credential_id_ts_idx ON public.request_logs_2026_06 USING btree (credential_id, ts DESC);


--
-- Name: request_logs_2026_06_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_failure_stage_failure_detail_code_ts_idx ON public.request_logs_2026_06 USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_session_id_ts_idx ON public.request_logs_2026_06 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_session_id_ts_idx1 ON public.request_logs_2026_06 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_06_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_gw_task_id_ts_idx ON public.request_logs_2026_06 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_06_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_identity_hash_ts_idx ON public.request_logs_2026_06 USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_2026_06_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_identity_hash_ts_idx1 ON public.request_logs_2026_06 USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_2026_06_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_2026_06 USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_2026_06_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_is_auto_request_task_type_ts_idx ON public.request_logs_2026_06 USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_2026_06_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_model_chosen_ts_idx ON public.request_logs_2026_06 USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_2026_06_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_owner_user_ts_idx ON public.request_logs_2026_06 USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_2026_06_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_parent_request_id_ts_idx ON public.request_logs_2026_06 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_06_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_provider_id_quality_score_ts_idx ON public.request_logs_2026_06 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_06_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_provider_id_ts_idx ON public.request_logs_2026_06 USING btree (provider_id, ts DESC);


--
-- Name: request_logs_2026_06_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_provider_id_ts_idx1 ON public.request_logs_2026_06 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_06_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_quality_flags_idx ON public.request_logs_2026_06 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_06_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_request_id_ts_idx ON public.request_logs_2026_06 USING btree (request_id, ts DESC);


--
-- Name: request_logs_2026_06_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_06_request_id_ts_idx1 ON public.request_logs_2026_06 USING btree (request_id, ts);


--
-- Name: request_logs_2026_06_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_request_status_ts_idx ON public.request_logs_2026_06 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_06_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_stream_interrupted_ts_idx ON public.request_logs_2026_06 USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_2026_06_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_06 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_06_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_ts_idx ON public.request_logs_2026_06 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_06_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tenant_id_ts_idx1 ON public.request_logs_2026_06 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_06_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_tool_calls_idx ON public.request_logs_2026_06 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_06_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_ts_idx ON public.request_logs_2026_06 USING btree (ts DESC);


--
-- Name: request_logs_2026_06_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_upstream_finish_reason_ts_idx ON public.request_logs_2026_06 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_06_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_usage_source_ts_idx ON public.request_logs_2026_06 USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_2026_06_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_06_work_type_ts_idx ON public.request_logs_2026_06 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_07_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_api_key_id_ts_idx ON public.request_logs_2026_07 USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_2026_07_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_client_model_ts_idx ON public.request_logs_2026_07 USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_2026_07_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_credential_id_ts_idx ON public.request_logs_2026_07 USING btree (credential_id, ts DESC);


--
-- Name: request_logs_2026_07_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_failure_stage_failure_detail_code_ts_idx ON public.request_logs_2026_07 USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_session_id_ts_idx ON public.request_logs_2026_07 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_session_id_ts_idx1 ON public.request_logs_2026_07 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_07_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_gw_task_id_ts_idx ON public.request_logs_2026_07 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_07_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_identity_hash_ts_idx ON public.request_logs_2026_07 USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_2026_07_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_identity_hash_ts_idx1 ON public.request_logs_2026_07 USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_2026_07_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_2026_07 USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_2026_07_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_is_auto_request_task_type_ts_idx ON public.request_logs_2026_07 USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_2026_07_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_model_chosen_ts_idx ON public.request_logs_2026_07 USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_2026_07_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_owner_user_ts_idx ON public.request_logs_2026_07 USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_2026_07_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_parent_request_id_ts_idx ON public.request_logs_2026_07 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_07_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_provider_id_quality_score_ts_idx ON public.request_logs_2026_07 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_07_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_provider_id_ts_idx ON public.request_logs_2026_07 USING btree (provider_id, ts DESC);


--
-- Name: request_logs_2026_07_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_provider_id_ts_idx1 ON public.request_logs_2026_07 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_07_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_quality_flags_idx ON public.request_logs_2026_07 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_07_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_request_id_ts_idx ON public.request_logs_2026_07 USING btree (request_id, ts DESC);


--
-- Name: request_logs_2026_07_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_07_request_id_ts_idx1 ON public.request_logs_2026_07 USING btree (request_id, ts);


--
-- Name: request_logs_2026_07_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_request_status_ts_idx ON public.request_logs_2026_07 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_07_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_stream_interrupted_ts_idx ON public.request_logs_2026_07 USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_2026_07_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_07 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_07_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_ts_idx ON public.request_logs_2026_07 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_07_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tenant_id_ts_idx1 ON public.request_logs_2026_07 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_07_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_tool_calls_idx ON public.request_logs_2026_07 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_07_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_ts_idx ON public.request_logs_2026_07 USING btree (ts DESC);


--
-- Name: request_logs_2026_07_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_upstream_finish_reason_ts_idx ON public.request_logs_2026_07 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_07_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_usage_source_ts_idx ON public.request_logs_2026_07 USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_2026_07_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_07_work_type_ts_idx ON public.request_logs_2026_07 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_2026_08_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_api_key_id_ts_idx ON public.request_logs_2026_08 USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_2026_08_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_client_model_ts_idx ON public.request_logs_2026_08 USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_2026_08_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_credential_id_ts_idx ON public.request_logs_2026_08 USING btree (credential_id, ts DESC);


--
-- Name: request_logs_2026_08_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_failure_stage_failure_detail_code_ts_idx ON public.request_logs_2026_08 USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_session_id_ts_idx ON public.request_logs_2026_08 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_session_id_ts_idx1 ON public.request_logs_2026_08 USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_2026_08_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_gw_task_id_ts_idx ON public.request_logs_2026_08 USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_08_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_identity_hash_ts_idx ON public.request_logs_2026_08 USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_2026_08_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_identity_hash_ts_idx1 ON public.request_logs_2026_08 USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_2026_08_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_2026_08 USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_2026_08_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_is_auto_request_task_type_ts_idx ON public.request_logs_2026_08 USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_2026_08_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_model_chosen_ts_idx ON public.request_logs_2026_08 USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_2026_08_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_owner_user_ts_idx ON public.request_logs_2026_08 USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_2026_08_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_parent_request_id_ts_idx ON public.request_logs_2026_08 USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_2026_08_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_provider_id_quality_score_ts_idx ON public.request_logs_2026_08 USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_2026_08_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_provider_id_ts_idx ON public.request_logs_2026_08 USING btree (provider_id, ts DESC);


--
-- Name: request_logs_2026_08_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_provider_id_ts_idx1 ON public.request_logs_2026_08 USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_2026_08_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_quality_flags_idx ON public.request_logs_2026_08 USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_2026_08_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_request_id_ts_idx ON public.request_logs_2026_08 USING btree (request_id, ts DESC);


--
-- Name: request_logs_2026_08_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_2026_08_request_id_ts_idx1 ON public.request_logs_2026_08 USING btree (request_id, ts);


--
-- Name: request_logs_2026_08_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_request_status_ts_idx ON public.request_logs_2026_08 USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_2026_08_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_stream_interrupted_ts_idx ON public.request_logs_2026_08 USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_2026_08_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_gw_task_id_ts_idx ON public.request_logs_2026_08 USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_2026_08_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_ts_idx ON public.request_logs_2026_08 USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_2026_08_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tenant_id_ts_idx1 ON public.request_logs_2026_08 USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_2026_08_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_tool_calls_idx ON public.request_logs_2026_08 USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_2026_08_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_ts_idx ON public.request_logs_2026_08 USING btree (ts DESC);


--
-- Name: request_logs_2026_08_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_upstream_finish_reason_ts_idx ON public.request_logs_2026_08 USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_2026_08_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_usage_source_ts_idx ON public.request_logs_2026_08 USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_2026_08_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_2026_08_work_type_ts_idx ON public.request_logs_2026_08 USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_logs_default_api_key_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_api_key_id_ts_idx ON public.request_logs_default USING btree (api_key_id, ts DESC);


--
-- Name: request_logs_default_client_model_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_client_model_ts_idx ON public.request_logs_default USING btree (client_model, ts DESC) WHERE ((is_auto_request = false) AND (client_model IS NOT NULL) AND (client_model <> ''::text));


--
-- Name: request_logs_default_credential_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_credential_id_ts_idx ON public.request_logs_default USING btree (credential_id, ts DESC);


--
-- Name: request_logs_default_failure_stage_failure_detail_code_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_failure_stage_failure_detail_code_ts_idx ON public.request_logs_default USING btree (failure_stage, failure_detail_code, ts DESC);


--
-- Name: request_logs_default_gw_session_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_session_id_ts_idx ON public.request_logs_default USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (gw_session_id <> ''::text));


--
-- Name: request_logs_default_gw_session_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_session_id_ts_idx1 ON public.request_logs_default USING btree (gw_session_id, ts DESC) WHERE ((gw_session_id IS NOT NULL) AND (outbound_body IS NOT NULL));


--
-- Name: request_logs_default_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_gw_task_id_ts_idx ON public.request_logs_default USING btree (gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_default_identity_hash_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_identity_hash_ts_idx ON public.request_logs_default USING btree (identity_hash, ts DESC) WHERE (identity_hash IS NOT NULL);


--
-- Name: request_logs_default_identity_hash_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_identity_hash_ts_idx1 ON public.request_logs_default USING btree (identity_hash, ts DESC);


--
-- Name: request_logs_default_is_auto_request_task_type_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_is_auto_request_task_type_chosen_ts_idx ON public.request_logs_default USING btree (is_auto_request, task_type_chosen, ts DESC) WHERE (is_auto_request = true);


--
-- Name: request_logs_default_is_auto_request_task_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_is_auto_request_task_type_ts_idx ON public.request_logs_default USING btree (is_auto_request, task_type, ts DESC);


--
-- Name: request_logs_default_model_chosen_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_model_chosen_ts_idx ON public.request_logs_default USING btree (model_chosen, ts DESC) WHERE (model_chosen IS NOT NULL);


--
-- Name: request_logs_default_owner_user_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_owner_user_ts_idx ON public.request_logs_default USING btree (owner_user, ts DESC) WHERE ((owner_user IS NOT NULL) AND (owner_user <> ''::text));


--
-- Name: request_logs_default_parent_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_parent_request_id_ts_idx ON public.request_logs_default USING btree (parent_request_id, ts DESC) WHERE (parent_request_id IS NOT NULL);


--
-- Name: request_logs_default_provider_id_quality_score_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_provider_id_quality_score_ts_idx ON public.request_logs_default USING btree (provider_id, quality_score, ts DESC) WHERE (quality_score IS NOT NULL);


--
-- Name: request_logs_default_provider_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_provider_id_ts_idx ON public.request_logs_default USING btree (provider_id, ts DESC);


--
-- Name: request_logs_default_provider_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_provider_id_ts_idx1 ON public.request_logs_default USING btree (provider_id, ts DESC) WHERE ((tool_calls IS NOT NULL) AND (jsonb_array_length(tool_calls) > 0));


--
-- Name: request_logs_default_quality_flags_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_quality_flags_idx ON public.request_logs_default USING gin (quality_flags) WHERE (cardinality(quality_flags) > 0);


--
-- Name: request_logs_default_request_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_request_id_ts_idx ON public.request_logs_default USING btree (request_id, ts DESC);


--
-- Name: request_logs_default_request_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX request_logs_default_request_id_ts_idx1 ON public.request_logs_default USING btree (request_id, ts);


--
-- Name: request_logs_default_request_status_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_request_status_ts_idx ON public.request_logs_default USING btree (request_status, ts DESC) WHERE ((request_status IS NOT NULL) AND (request_status <> ''::text));


--
-- Name: request_logs_default_stream_interrupted_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_stream_interrupted_ts_idx ON public.request_logs_default USING btree (stream_interrupted, ts DESC);


--
-- Name: request_logs_default_tenant_id_gw_task_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_gw_task_id_ts_idx ON public.request_logs_default USING btree (tenant_id, gw_task_id, ts DESC) WHERE ((gw_task_id IS NOT NULL) AND (gw_task_id <> ''::text));


--
-- Name: request_logs_default_tenant_id_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_ts_idx ON public.request_logs_default USING btree (tenant_id, ts DESC) WHERE ((credits_charged IS NOT NULL) AND (credits_charged > 0));


--
-- Name: request_logs_default_tenant_id_ts_idx1; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tenant_id_ts_idx1 ON public.request_logs_default USING btree (tenant_id, ts DESC) WHERE ((outbound_msg_count IS NOT NULL) AND (outbound_msg_count > 0));


--
-- Name: request_logs_default_tool_calls_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_tool_calls_idx ON public.request_logs_default USING gin (tool_calls) WHERE ((tool_calls IS NOT NULL) AND (tool_calls <> '[]'::jsonb));


--
-- Name: request_logs_default_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_ts_idx ON public.request_logs_default USING btree (ts DESC);


--
-- Name: request_logs_default_upstream_finish_reason_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_upstream_finish_reason_ts_idx ON public.request_logs_default USING btree (upstream_finish_reason, ts DESC) WHERE ((upstream_finish_reason IS NOT NULL) AND (upstream_finish_reason <> ''::text));


--
-- Name: request_logs_default_usage_source_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_usage_source_ts_idx ON public.request_logs_default USING btree (usage_source, ts DESC) WHERE (usage_source = 'estimated'::text);


--
-- Name: request_logs_default_work_type_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_logs_default_work_type_ts_idx ON public.request_logs_default USING btree (work_type, ts DESC) WHERE ((work_type IS NOT NULL) AND (work_type <> ''::text));


--
-- Name: request_wal_2026_06_gw_session_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_gw_session_id_created_at_idx ON public.request_wal_2026_06 USING btree (gw_session_id, created_at);


--
-- Name: request_wal_2026_06_status_stage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_status_stage_idx ON public.request_wal_2026_06 USING btree (status, stage);


--
-- Name: request_wal_2026_06_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_06_tenant_id_created_at_idx ON public.request_wal_2026_06 USING btree (tenant_id, created_at DESC);


--
-- Name: request_wal_2026_07_gw_session_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_gw_session_id_created_at_idx ON public.request_wal_2026_07 USING btree (gw_session_id, created_at);


--
-- Name: request_wal_2026_07_status_stage_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_status_stage_idx ON public.request_wal_2026_07 USING btree (status, stage);


--
-- Name: request_wal_2026_07_tenant_id_created_at_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX request_wal_2026_07_tenant_id_created_at_idx ON public.request_wal_2026_07 USING btree (tenant_id, created_at DESC);


--
-- Name: routing_decision_log_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX routing_decision_log_ts_idx ON public.routing_decision_log USING btree (ts DESC);


--
-- Name: token_audit_events_ts_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX token_audit_events_ts_idx ON public.token_audit_events USING btree (ts DESC);


--
-- Name: uq_model_aliases_raw_quant_surface; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_model_aliases_raw_quant_surface ON public.model_aliases USING btree (raw_name, COALESCE(quantization, ''::text), COALESCE(surface, ''::text));


--
-- Name: uq_model_discovery_runs_one_running; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_model_discovery_runs_one_running ON public.model_discovery_runs USING btree (tenant_id) WHERE (status = 'running'::text);


--
-- Name: uq_usage_minute_dims; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX uq_usage_minute_dims ON public.usage_minute USING btree (bucket, tenant_id, COALESCE(application_id, (0)::bigint), COALESCE(api_key_id, (0)::bigint), COALESCE(end_user_id, ''::text), COALESCE(department, ''::text), COALESCE(employee, ''::text), COALESCE("position", ''::text), COALESCE(credential_id, (0)::bigint), COALESCE(provider_id, (0)::bigint), COALESCE(canonical_id, (0)::bigint));


--
-- Name: usage_minute_bucket_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_minute_bucket_idx ON public.usage_minute USING btree (bucket DESC);


--
-- Name: request_logs_2026_04_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_2026_04_api_key_id_ts_idx;


--
-- Name: request_logs_2026_04_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_2026_04_client_model_ts_idx;


--
-- Name: request_logs_2026_04_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_2026_04_credential_id_ts_idx;


--
-- Name: request_logs_2026_04_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_2026_04_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_2026_04_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_04_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_04_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_04_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_04_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_04_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_04_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_2026_04_identity_hash_ts_idx;


--
-- Name: request_logs_2026_04_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_2026_04_identity_hash_ts_idx1;


--
-- Name: request_logs_2026_04_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_2026_04_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_2026_04_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_2026_04_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_2026_04_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_2026_04_model_chosen_ts_idx;


--
-- Name: request_logs_2026_04_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_2026_04_owner_user_ts_idx;


--
-- Name: request_logs_2026_04_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_04_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_04_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_2026_04_pkey;


--
-- Name: request_logs_2026_04_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_04_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_04_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_2026_04_provider_id_ts_idx;


--
-- Name: request_logs_2026_04_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_04_provider_id_ts_idx1;


--
-- Name: request_logs_2026_04_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_04_quality_flags_idx;


--
-- Name: request_logs_2026_04_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_2026_04_request_id_ts_idx;


--
-- Name: request_logs_2026_04_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_2026_04_request_id_ts_idx1;


--
-- Name: request_logs_2026_04_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_04_request_status_ts_idx;


--
-- Name: request_logs_2026_04_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_2026_04_stream_interrupted_ts_idx;


--
-- Name: request_logs_2026_04_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_04_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_04_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_04_tenant_id_ts_idx;


--
-- Name: request_logs_2026_04_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_04_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_04_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_04_tool_calls_idx;


--
-- Name: request_logs_2026_04_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_2026_04_ts_idx;


--
-- Name: request_logs_2026_04_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_04_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_04_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_2026_04_usage_source_ts_idx;


--
-- Name: request_logs_2026_04_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_04_work_type_ts_idx;


--
-- Name: request_logs_2026_05_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_2026_05_api_key_id_ts_idx;


--
-- Name: request_logs_2026_05_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_2026_05_client_model_ts_idx;


--
-- Name: request_logs_2026_05_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_2026_05_credential_id_ts_idx;


--
-- Name: request_logs_2026_05_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_2026_05_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_2026_05_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_05_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_05_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_05_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_05_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_05_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_05_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_2026_05_identity_hash_ts_idx;


--
-- Name: request_logs_2026_05_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_2026_05_identity_hash_ts_idx1;


--
-- Name: request_logs_2026_05_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_2026_05_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_2026_05_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_2026_05_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_2026_05_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_2026_05_model_chosen_ts_idx;


--
-- Name: request_logs_2026_05_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_2026_05_owner_user_ts_idx;


--
-- Name: request_logs_2026_05_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_05_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_05_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_2026_05_pkey;


--
-- Name: request_logs_2026_05_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_05_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_05_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_2026_05_provider_id_ts_idx;


--
-- Name: request_logs_2026_05_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_05_provider_id_ts_idx1;


--
-- Name: request_logs_2026_05_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_05_quality_flags_idx;


--
-- Name: request_logs_2026_05_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_2026_05_request_id_ts_idx;


--
-- Name: request_logs_2026_05_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_2026_05_request_id_ts_idx1;


--
-- Name: request_logs_2026_05_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_05_request_status_ts_idx;


--
-- Name: request_logs_2026_05_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_2026_05_stream_interrupted_ts_idx;


--
-- Name: request_logs_2026_05_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_05_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_05_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_05_tenant_id_ts_idx;


--
-- Name: request_logs_2026_05_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_05_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_05_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_05_tool_calls_idx;


--
-- Name: request_logs_2026_05_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_2026_05_ts_idx;


--
-- Name: request_logs_2026_05_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_05_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_05_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_2026_05_usage_source_ts_idx;


--
-- Name: request_logs_2026_05_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_05_work_type_ts_idx;


--
-- Name: request_logs_2026_06_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_2026_06_api_key_id_ts_idx;


--
-- Name: request_logs_2026_06_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_2026_06_client_model_ts_idx;


--
-- Name: request_logs_2026_06_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_2026_06_credential_id_ts_idx;


--
-- Name: request_logs_2026_06_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_2026_06_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_06_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_06_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_06_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_06_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_06_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_06_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_2026_06_identity_hash_ts_idx;


--
-- Name: request_logs_2026_06_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_2026_06_identity_hash_ts_idx1;


--
-- Name: request_logs_2026_06_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_2026_06_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_2026_06_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_2026_06_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_2026_06_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_2026_06_model_chosen_ts_idx;


--
-- Name: request_logs_2026_06_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_2026_06_owner_user_ts_idx;


--
-- Name: request_logs_2026_06_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_06_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_06_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_2026_06_pkey;


--
-- Name: request_logs_2026_06_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_06_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_06_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_2026_06_provider_id_ts_idx;


--
-- Name: request_logs_2026_06_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_06_provider_id_ts_idx1;


--
-- Name: request_logs_2026_06_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_06_quality_flags_idx;


--
-- Name: request_logs_2026_06_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_2026_06_request_id_ts_idx;


--
-- Name: request_logs_2026_06_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_2026_06_request_id_ts_idx1;


--
-- Name: request_logs_2026_06_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_06_request_status_ts_idx;


--
-- Name: request_logs_2026_06_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_2026_06_stream_interrupted_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_06_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_06_tenant_id_ts_idx;


--
-- Name: request_logs_2026_06_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_06_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_06_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_06_tool_calls_idx;


--
-- Name: request_logs_2026_06_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_2026_06_ts_idx;


--
-- Name: request_logs_2026_06_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_06_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_06_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_2026_06_usage_source_ts_idx;


--
-- Name: request_logs_2026_06_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_06_work_type_ts_idx;


--
-- Name: request_logs_2026_07_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_2026_07_api_key_id_ts_idx;


--
-- Name: request_logs_2026_07_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_2026_07_client_model_ts_idx;


--
-- Name: request_logs_2026_07_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_2026_07_credential_id_ts_idx;


--
-- Name: request_logs_2026_07_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_2026_07_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_07_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_07_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_07_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_07_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_07_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_07_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_2026_07_identity_hash_ts_idx;


--
-- Name: request_logs_2026_07_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_2026_07_identity_hash_ts_idx1;


--
-- Name: request_logs_2026_07_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_2026_07_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_2026_07_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_2026_07_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_2026_07_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_2026_07_model_chosen_ts_idx;


--
-- Name: request_logs_2026_07_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_2026_07_owner_user_ts_idx;


--
-- Name: request_logs_2026_07_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_07_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_07_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_2026_07_pkey;


--
-- Name: request_logs_2026_07_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_07_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_07_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_2026_07_provider_id_ts_idx;


--
-- Name: request_logs_2026_07_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_07_provider_id_ts_idx1;


--
-- Name: request_logs_2026_07_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_07_quality_flags_idx;


--
-- Name: request_logs_2026_07_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_2026_07_request_id_ts_idx;


--
-- Name: request_logs_2026_07_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_2026_07_request_id_ts_idx1;


--
-- Name: request_logs_2026_07_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_07_request_status_ts_idx;


--
-- Name: request_logs_2026_07_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_2026_07_stream_interrupted_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_07_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_07_tenant_id_ts_idx;


--
-- Name: request_logs_2026_07_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_07_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_07_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_07_tool_calls_idx;


--
-- Name: request_logs_2026_07_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_2026_07_ts_idx;


--
-- Name: request_logs_2026_07_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_07_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_07_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_2026_07_usage_source_ts_idx;


--
-- Name: request_logs_2026_07_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_07_work_type_ts_idx;


--
-- Name: request_logs_2026_08_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_2026_08_api_key_id_ts_idx;


--
-- Name: request_logs_2026_08_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_2026_08_client_model_ts_idx;


--
-- Name: request_logs_2026_08_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_2026_08_credential_id_ts_idx;


--
-- Name: request_logs_2026_08_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_2026_08_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_2026_08_gw_session_id_ts_idx;


--
-- Name: request_logs_2026_08_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_2026_08_gw_session_id_ts_idx1;


--
-- Name: request_logs_2026_08_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_2026_08_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_08_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_2026_08_identity_hash_ts_idx;


--
-- Name: request_logs_2026_08_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_2026_08_identity_hash_ts_idx1;


--
-- Name: request_logs_2026_08_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_2026_08_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_2026_08_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_2026_08_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_2026_08_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_2026_08_model_chosen_ts_idx;


--
-- Name: request_logs_2026_08_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_2026_08_owner_user_ts_idx;


--
-- Name: request_logs_2026_08_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_2026_08_parent_request_id_ts_idx;


--
-- Name: request_logs_2026_08_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_2026_08_pkey;


--
-- Name: request_logs_2026_08_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_2026_08_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_2026_08_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_2026_08_provider_id_ts_idx;


--
-- Name: request_logs_2026_08_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_2026_08_provider_id_ts_idx1;


--
-- Name: request_logs_2026_08_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_2026_08_quality_flags_idx;


--
-- Name: request_logs_2026_08_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_2026_08_request_id_ts_idx;


--
-- Name: request_logs_2026_08_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_2026_08_request_id_ts_idx1;


--
-- Name: request_logs_2026_08_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_2026_08_request_status_ts_idx;


--
-- Name: request_logs_2026_08_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_2026_08_stream_interrupted_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_2026_08_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_2026_08_tenant_id_ts_idx;


--
-- Name: request_logs_2026_08_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_2026_08_tenant_id_ts_idx1;


--
-- Name: request_logs_2026_08_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_2026_08_tool_calls_idx;


--
-- Name: request_logs_2026_08_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_2026_08_ts_idx;


--
-- Name: request_logs_2026_08_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_2026_08_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_2026_08_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_2026_08_usage_source_ts_idx;


--
-- Name: request_logs_2026_08_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_2026_08_work_type_ts_idx;


--
-- Name: request_logs_default_api_key_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_api_key_ts ATTACH PARTITION public.request_logs_default_api_key_id_ts_idx;


--
-- Name: request_logs_default_client_model_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_explicit_model ATTACH PARTITION public.request_logs_default_client_model_ts_idx;


--
-- Name: request_logs_default_credential_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credential_ts ATTACH PARTITION public.request_logs_default_credential_id_ts_idx;


--
-- Name: request_logs_default_failure_stage_failure_detail_code_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_failure_ts ATTACH PARTITION public.request_logs_default_failure_stage_failure_detail_code_ts_idx;


--
-- Name: request_logs_default_gw_session_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_session_ts ATTACH PARTITION public.request_logs_default_gw_session_id_ts_idx;


--
-- Name: request_logs_default_gw_session_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_session_outbound ATTACH PARTITION public.request_logs_default_gw_session_id_ts_idx1;


--
-- Name: request_logs_default_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_gw_task_ts ATTACH PARTITION public.request_logs_default_gw_task_id_ts_idx;


--
-- Name: request_logs_default_identity_hash_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_hash ATTACH PARTITION public.request_logs_default_identity_hash_ts_idx;


--
-- Name: request_logs_default_identity_hash_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_identity_ts ATTACH PARTITION public.request_logs_default_identity_hash_ts_idx1;


--
-- Name: request_logs_default_is_auto_request_task_type_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_is_auto_request_task_ts ATTACH PARTITION public.request_logs_default_is_auto_request_task_type_chosen_ts_idx;


--
-- Name: request_logs_default_is_auto_request_task_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_auto ATTACH PARTITION public.request_logs_default_is_auto_request_task_type_ts_idx;


--
-- Name: request_logs_default_model_chosen_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_model_chosen_ts ATTACH PARTITION public.request_logs_default_model_chosen_ts_idx;


--
-- Name: request_logs_default_owner_user_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_owner_user_ts ATTACH PARTITION public.request_logs_default_owner_user_ts_idx;


--
-- Name: request_logs_default_parent_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_parent_ts ATTACH PARTITION public.request_logs_default_parent_request_id_ts_idx;


--
-- Name: request_logs_default_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_logs_pkey ATTACH PARTITION public.request_logs_default_pkey;


--
-- Name: request_logs_default_provider_id_quality_score_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_quality ATTACH PARTITION public.request_logs_default_provider_id_quality_score_ts_idx;


--
-- Name: request_logs_default_provider_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_ts ATTACH PARTITION public.request_logs_default_provider_id_ts_idx;


--
-- Name: request_logs_default_provider_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_provider_tool_calls ATTACH PARTITION public.request_logs_default_provider_id_ts_idx1;


--
-- Name: request_logs_default_quality_flags_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_quality_flags ATTACH PARTITION public.request_logs_default_quality_flags_idx;


--
-- Name: request_logs_default_request_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts ATTACH PARTITION public.request_logs_default_request_id_ts_idx;


--
-- Name: request_logs_default_request_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_request_id_ts_unique ATTACH PARTITION public.request_logs_default_request_id_ts_idx1;


--
-- Name: request_logs_default_request_status_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_status_ts ATTACH PARTITION public.request_logs_default_request_status_ts_idx;


--
-- Name: request_logs_default_stream_interrupted_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_stream_ts ATTACH PARTITION public.request_logs_default_stream_interrupted_ts_idx;


--
-- Name: request_logs_default_tenant_id_gw_task_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tenant_task_ts ATTACH PARTITION public.request_logs_default_tenant_id_gw_task_id_ts_idx;


--
-- Name: request_logs_default_tenant_id_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_credits_charged ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx;


--
-- Name: request_logs_default_tenant_id_ts_idx1; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_outbound_msg_count ATTACH PARTITION public.request_logs_default_tenant_id_ts_idx1;


--
-- Name: request_logs_default_tool_calls_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_tool_calls ATTACH PARTITION public.request_logs_default_tool_calls_idx;


--
-- Name: request_logs_default_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_ts ATTACH PARTITION public.request_logs_default_ts_idx;


--
-- Name: request_logs_default_upstream_finish_reason_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_upstream_finish_reason ATTACH PARTITION public.request_logs_default_upstream_finish_reason_ts_idx;


--
-- Name: request_logs_default_usage_source_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_usage_source_ts ATTACH PARTITION public.request_logs_default_usage_source_ts_idx;


--
-- Name: request_logs_default_work_type_ts_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_request_logs_work_type ATTACH PARTITION public.request_logs_default_work_type_ts_idx;


--
-- Name: request_wal_2026_06_gw_session_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_session ATTACH PARTITION public.request_wal_2026_06_gw_session_id_created_at_idx;


--
-- Name: request_wal_2026_06_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_wal_pkey ATTACH PARTITION public.request_wal_2026_06_pkey;


--
-- Name: request_wal_2026_06_status_stage_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_status_stage ATTACH PARTITION public.request_wal_2026_06_status_stage_idx;


--
-- Name: request_wal_2026_06_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_tenant_created ATTACH PARTITION public.request_wal_2026_06_tenant_id_created_at_idx;


--
-- Name: request_wal_2026_07_gw_session_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_session ATTACH PARTITION public.request_wal_2026_07_gw_session_id_created_at_idx;


--
-- Name: request_wal_2026_07_pkey; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.request_wal_pkey ATTACH PARTITION public.request_wal_2026_07_pkey;


--
-- Name: request_wal_2026_07_status_stage_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_status_stage ATTACH PARTITION public.request_wal_2026_07_status_stage_idx;


--
-- Name: request_wal_2026_07_tenant_id_created_at_idx; Type: INDEX ATTACH; Schema: public; Owner: -
--

ALTER INDEX public.idx_wal_tenant_created ATTACH PARTITION public.request_wal_2026_07_tenant_id_created_at_idx;


--
-- Name: credential_model_bindings cmb_protect_manual_disable; Type: TRIGGER; Schema: public; Owner: -
--

-- =============================================================================
-- DEFERRED FUNCTIONS — Created here (after all tables) to satisfy dependency
-- ordering that pg_dump --schema-only does not respect. See note above.
-- =============================================================================

-- recent_success_rate — moved from earlier in the dump.

CREATE FUNCTION public.recent_success_rate(p_credential_id bigint, p_raw_model text, p_sample_n integer DEFAULT 50, p_window_hours integer DEFAULT 3) RETURNS TABLE(rate double precision, samples integer)
    LANGUAGE sql STABLE
    AS $$
		    WITH recent AS (
		        SELECT success
		        FROM request_logs
		        WHERE credential_id = p_credential_id
		          AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
		          AND ts > NOW() - (p_window_hours || ' hours')::interval
		        ORDER BY ts DESC
		        LIMIT p_sample_n
		    )
		    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
		           COUNT(*)::int
		    FROM recent;
		$$;


--
-- Name: routing_overrides_audit_fn(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.routing_overrides_audit_fn() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('insert', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		             NEW.model_chosen, NEW.reason, NEW.expires_at, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.expires_at IS DISTINCT FROM OLD.expires_at
		           OR NEW.reason IS DISTINCT FROM OLD.reason
		           OR NEW.model_chosen IS DISTINCT FROM OLD.model_chosen
		        THEN
		            INSERT INTO routing_overrides_audit
		                (action, override_id, task_type, profile, mode,
		                 model_chosen, reason, expires_at, old_expires_at,
		                 actor)
		            VALUES
		                ('update', NEW.id, NEW.task_type, NEW.profile, NEW.mode,
		                 NEW.model_chosen, NEW.reason, NEW.expires_at,
		                 OLD.expires_at, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO routing_overrides_audit
		            (action, override_id, task_type, profile, mode,
		             model_chosen, reason, expires_at, actor)
		        VALUES
		            ('delete', OLD.id, OLD.task_type, OLD.profile, OLD.mode,
		             OLD.model_chosen, OLD.reason, OLD.expires_at, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$;


--
-- Name: tenant_model_policies_audit_fn(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.tenant_model_policies_audit_fn() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    v_actor TEXT := COALESCE(
		        NULLIF(current_setting('app.current_admin', true), ''),
		        'system'
		    );
		BEGIN
		    IF (TG_OP = 'INSERT') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('insert', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        RETURN NEW;
		    ELSIF (TG_OP = 'UPDATE') THEN
		        IF NEW.deleted_at IS DISTINCT FROM OLD.deleted_at THEN
		            IF NEW.deleted_at IS NULL THEN
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('undelete', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		            ELSE
		                INSERT INTO tenant_model_policies_audit
		                    (action, policy_id, tenant_id, canonical_name, reason, actor)
		                VALUES
		                    ('delete', NEW.id, NEW.tenant_id, NEW.canonical_name, OLD.reason, v_actor);
		            END IF;
		        ELSIF NEW.reason IS DISTINCT FROM OLD.reason
		              OR NEW.canonical_name IS DISTINCT FROM OLD.canonical_name
		        THEN
		            INSERT INTO tenant_model_policies_audit
		                (action, policy_id, tenant_id, canonical_name, reason, actor)
		            VALUES
		                ('update', NEW.id, NEW.tenant_id, NEW.canonical_name, NEW.reason, v_actor);
		        END IF;
		        RETURN NEW;
		    ELSIF (TG_OP = 'DELETE') THEN
		        INSERT INTO tenant_model_policies_audit
		            (action, policy_id, tenant_id, canonical_name, reason, actor)
		        VALUES
		            ('delete', OLD.id, OLD.tenant_id, OLD.canonical_name, OLD.reason, v_actor);
		        RETURN OLD;
		    END IF;
		    RETURN NULL;
		END;
		$$;


--
-- Name: trg_cmb_protect_manual_disable(); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.trg_cmb_protect_manual_disable() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    IF OLD.unavailable_reason = 'manual' THEN
        -- Admin explicit re-enable (toggleModelOfferState available=true)
        IF (NEW.available = TRUE AND NEW.unavailable_reason IS NULL)
           OR current_setting('llmgw.admin_override', true) = '1' THEN
            RETURN NEW;
        END IF;

        IF NEW.unavailable_reason IS DISTINCT FROM 'manual' THEN
            NEW.unavailable_reason := 'manual';
        END IF;
        IF NEW.available = TRUE THEN
            NEW.available := FALSE;
        END IF;
        IF NEW.unavailable_at IS NULL THEN
            NEW.unavailable_at := OLD.unavailable_at;
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER cmb_protect_manual_disable BEFORE UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.trg_cmb_protect_manual_disable();

ALTER TABLE public.credential_model_bindings DISABLE TRIGGER cmb_protect_manual_disable;


--
-- Name: model_offers model_offers_delete; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_delete INSTEAD OF DELETE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_delete_trigger();


--
-- Name: model_offers model_offers_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_insert INSTEAD OF INSERT ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_insert_trigger();


--
-- Name: model_offers model_offers_update; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER model_offers_update INSTEAD OF UPDATE ON public.model_offers FOR EACH ROW EXECUTE FUNCTION public.model_offers_update_trigger();


--
-- Name: routing_overrides routing_overrides_audit_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER routing_overrides_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.routing_overrides FOR EACH ROW EXECUTE FUNCTION public.routing_overrides_audit_fn();


--
-- Name: tenant_model_policies tenant_model_policies_audit_trg; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER tenant_model_policies_audit_trg AFTER INSERT OR DELETE OR UPDATE ON public.tenant_model_policies FOR EACH ROW EXECUTE FUNCTION public.tenant_model_policies_audit_fn();


--
-- Name: credentials trg_auto_fp_slot_limit_insert; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_auto_fp_slot_limit_insert BEFORE INSERT ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.auto_set_fp_slot_limit();

ALTER TABLE public.credentials DISABLE TRIGGER trg_auto_fp_slot_limit_insert;


--
-- Name: credentials trg_check_credential_dates; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_check_credential_dates BEFORE INSERT OR UPDATE ON public.credentials FOR EACH ROW EXECUTE FUNCTION public.check_credential_dates();

ALTER TABLE public.credentials DISABLE TRIGGER trg_check_credential_dates;


--
-- Name: key_applications trg_key_applications_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_key_applications_updated_at BEFORE UPDATE ON public.key_applications FOR EACH ROW EXECUTE FUNCTION public.key_applications_set_updated_at();

ALTER TABLE public.key_applications DISABLE TRIGGER trg_key_applications_updated_at;


--
-- Name: api_keys trg_notify_auto_route_apikeys; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_apikeys AFTER UPDATE OF rate_limit_rpm, budget_usd, enabled, status ON public.api_keys FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();

ALTER TABLE public.api_keys DISABLE TRIGGER trg_notify_auto_route_apikeys;


--
-- Name: credential_model_bindings trg_notify_auto_route_cmb; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_cmb AFTER INSERT OR DELETE OR UPDATE ON public.credential_model_bindings FOR EACH ROW EXECUTE FUNCTION public.notify_auto_route_refresh();

ALTER TABLE public.credential_model_bindings DISABLE TRIGGER trg_notify_auto_route_cmb;


--
-- Name: credentials trg_notify_auto_route_creds; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_notify_auto_route_creds AFTER UPDATE OF status, availability_state, quota_state, circuit_state, concurrency_limit, lifecycle_status ON public.credentials FOR EACH ROW WHEN ((old.* IS DISTINCT FROM new.*)) EXECUTE FUNCTION public.notify_auto_route_refresh();

ALTER TABLE public.credentials DISABLE TRIGGER trg_notify_auto_route_creds;


--
-- Name: request_logs trg_update_api_key_model_cost; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trg_update_api_key_model_cost AFTER INSERT ON public.request_logs FOR EACH ROW WHEN ((new.is_auto_request = true)) EXECUTE FUNCTION public.update_api_key_model_cost();

ALTER TABLE public.request_logs DISABLE TRIGGER trg_update_api_key_model_cost;


--
-- Name: provider_settings trigger_provider_settings_updated_at; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER trigger_provider_settings_updated_at BEFORE UPDATE ON public.provider_settings FOR EACH ROW EXECUTE FUNCTION public.update_provider_settings_updated_at();

ALTER TABLE public.provider_settings DISABLE TRIGGER trigger_provider_settings_updated_at;


--
-- Name: billing_orders; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.billing_orders ENABLE ROW LEVEL SECURITY;

--
-- Name: credit_ledger; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.credit_ledger ENABLE ROW LEVEL SECURITY;

--
-- Name: model_probe_runs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.model_probe_runs ENABLE ROW LEVEL SECURITY;

--
-- Name: request_logs; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.request_logs ENABLE ROW LEVEL SECURITY;

--
-- Name: settings_audit; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.settings_audit ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_credit_wallets; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_credit_wallets ENABLE ROW LEVEL SECURITY;

--
-- Name: billing_orders tenant_isolation_billing_orders; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_billing_orders ON public.billing_orders USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: credit_ledger tenant_isolation_credit_ledger; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_credit_ledger ON public.credit_ledger USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: model_probe_runs tenant_isolation_model_probe_runs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_model_probe_runs ON public.model_probe_runs USING ((tenant_id = public.get_current_tenant()));


--
-- Name: POLICY tenant_isolation_model_probe_runs ON model_probe_runs; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON POLICY tenant_isolation_model_probe_runs ON public.model_probe_runs IS 'Round 47 (2026-06-18): per-tenant isolation for probe history. Closes L1 leak discovered by lint-pg-rls during v7 T1 prep. Required by docs/multi-tenant-standards.md §3.2 (Pattern A: tenant_id column requires ENABLE ROW LEVEL SECURITY).';


--
-- Name: request_logs tenant_isolation_request_logs; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_request_logs ON public.request_logs USING ((tenant_id = public.get_current_tenant()));


--
-- Name: settings_audit tenant_isolation_settings_audit; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_settings_audit ON public.settings_audit USING ((((tenant_id)::text = public.get_current_tenant()) OR (tenant_id IS NULL)));


--
-- Name: tenant_credit_wallets tenant_isolation_tenant_credit_wallets; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_credit_wallets ON public.tenant_credit_wallets USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_settings_kv tenant_isolation_tenant_settings_kv; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_settings_kv ON public.tenant_settings_kv USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_subscriptions tenant_isolation_tenant_subscriptions; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_subscriptions ON public.tenant_subscriptions USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_tool_policies tenant_isolation_tenant_tool_policies; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tenant_tool_policies ON public.tenant_tool_policies USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies tenant_isolation_tmp; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tmp ON public.tenant_model_policies USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies_audit tenant_isolation_tmp_audit; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tmp_audit ON public.tenant_model_policies_audit USING (((tenant_id = public.get_current_tenant()) OR (tenant_id IS NULL)));


--
-- Name: tool_call_events tenant_isolation_tool_call_events; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_call_events ON public.tool_call_events USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tool_registry tenant_isolation_tool_registry; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_registry ON public.tool_registry USING ((((tenant_id)::text = public.get_current_tenant()) OR (tenant_id IS NULL) OR ((tenant_id)::text = 'default'::text)));


--
-- Name: tool_usage_stats tenant_isolation_tool_usage_stats; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_tool_usage_stats ON public.tool_usage_stats USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: users tenant_isolation_users; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY tenant_isolation_users ON public.users USING (((tenant_id)::text = public.get_current_tenant()));


--
-- Name: tenant_model_policies; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_model_policies ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_model_policies_audit; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_model_policies_audit ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_settings_kv; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_settings_kv ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_subscriptions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_subscriptions ENABLE ROW LEVEL SECURITY;

--
-- Name: tenant_tool_policies; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tenant_tool_policies ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_call_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_call_events ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_registry; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_registry ENABLE ROW LEVEL SECURITY;

--
-- Name: tool_usage_stats; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.tool_usage_stats ENABLE ROW LEVEL SECURITY;

--
-- Name: users; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;

--
--
