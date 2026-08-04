--
-- Name: unified_probe_mark_healthy(bigint, text, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.unified_probe_mark_healthy(p_credential_id bigint, p_raw_model_name text, p_latency_ms integer DEFAULT 0) RETURNS void
    LANGUAGE plpgsql
    AS $$
		DECLARE
		    new_interval INTERVAL;
		BEGIN
		    SELECT CASE
		        WHEN consecutive_watchdog_successes >= 10 THEN '8 hours'::INTERVAL
		        WHEN consecutive_watchdog_successes >= 5 THEN '6 hours'::INTERVAL
		        WHEN consecutive_watchdog_successes >= 2 THEN '4 hours'::INTERVAL
		        ELSE '2 hours'::INTERVAL
		    END INTO new_interval
		    FROM model_probe_state
		    WHERE credential_id = p_credential_id
		      AND raw_model_name = p_raw_model_name;

		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, last_verified_at, next_retry_at,
		         probe_priority, verification_interval,
		         consecutive_watchdog_successes,
		         last_status, probing_started_at)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'healthy',
		         1, 0,
		         NOW(), NOW(), NOW() + COALESCE(new_interval, '4 hours'::INTERVAL),
		         'watchdog', COALESCE(new_interval, '4 hours'::INTERVAL),
		         1,
		         'ok', NULL)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'healthy',
		        consecutive_successes = model_probe_state.consecutive_successes + 1,
		        consecutive_failures = 0,
		        last_attempt_at = NOW(),
		        last_verified_at = NOW(),
		        next_retry_at = NOW() + COALESCE(new_interval, model_probe_state.verification_interval, '4 hours'::INTERVAL),
		        probe_priority = 'watchdog',
		        verification_interval = COALESCE(new_interval, model_probe_state.verification_interval),
		        consecutive_watchdog_successes = CASE
		            WHEN model_probe_state.probe_priority = 'watchdog' THEN model_probe_state.consecutive_watchdog_successes + 1
		            ELSE 1
		        END,
		        last_status = 'ok',
		        probing_started_at = NULL,
		        state_expires_at = NULL,
		        marked_suspicious_at = NULL;

		    UPDATE credential_model_bindings cmb
		    SET available = TRUE,
		        unavailable_reason = NULL,
		        unavailable_at = NULL,
		        unavailable_recover_at = NULL,
		        updated_at = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';
		END;
		$$;

