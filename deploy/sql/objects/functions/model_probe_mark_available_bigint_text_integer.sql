--
-- Name: model_probe_mark_available(bigint, text, integer); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_mark_available(p_credential_id bigint, p_raw_model_name text, p_latency_ms integer DEFAULT 0) RETURNS void
    LANGUAGE plpgsql
    AS $$
		BEGIN
		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, next_retry_at, last_status,
		         state_expires_at, marked_suspicious_at)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'available',
		         1, 0,
		         NOW(), NOW() + INTERVAL '2 hours', 'ok',
		         NOW() + INTERVAL '2 hours', NULL)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'available',
		        consecutive_successes = model_probe_state.consecutive_successes + 1,
		        consecutive_failures = 0,
		        last_attempt_at = NOW(),
		        next_retry_at = NOW() + INTERVAL '2 hours',
		        last_status = 'ok',
		        state_expires_at = NOW() + INTERVAL '2 hours',
		        marked_suspicious_at = NULL,
		        probing_started_at = NULL;

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

