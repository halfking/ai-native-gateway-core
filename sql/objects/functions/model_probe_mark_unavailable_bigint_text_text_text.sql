--
-- Name: model_probe_mark_unavailable(bigint, text, text, text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.model_probe_mark_unavailable(p_credential_id bigint, p_raw_model_name text, p_error_code text, p_error_message text DEFAULT ''::text) RETURNS void
    LANGUAGE plpgsql
    AS $$
		BEGIN
		    INSERT INTO model_probe_state
		        (credential_id, raw_model_name, state,
		         consecutive_successes, consecutive_failures,
		         last_attempt_at, next_retry_at, last_status,
		         state_expires_at, marked_suspicious_at,
		         last_unavailable_reason, last_err_code)
		    VALUES
		        (p_credential_id, p_raw_model_name, 'unavailable',
		         0, 1,
		         NOW(), NOW() + INTERVAL '15 minutes', 'http_4xx',
		         NOW() + INTERVAL '15 minutes', NULL,
		         p_error_message, p_error_code)
		    ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
		        state = 'unavailable',
		        consecutive_successes = 0,
		        consecutive_failures = model_probe_state.consecutive_failures + 1,
		        last_attempt_at = NOW(),
		        next_retry_at = NOW() + INTERVAL '15 minutes',
		        last_status = 'http_4xx',
		        state_expires_at = NOW() + INTERVAL '15 minutes',
		        marked_suspicious_at = NULL,
		        probing_started_at = NULL,
		        last_unavailable_reason = p_error_message,
		        last_err_code = p_error_code;

		    UPDATE credential_model_bindings cmb
		    SET available = FALSE,
		        unavailable_reason = 'probe_' || p_error_code,
		        unavailable_at = NOW(),
		        unavailable_recover_at = NOW() + INTERVAL '15 minutes',
		        updated_at = NOW()
		    FROM provider_models pm
		    WHERE cmb.provider_model_id = pm.id
		      AND cmb.credential_id = p_credential_id
		      AND pm.raw_model_name = p_raw_model_name
		      AND COALESCE(cmb.unavailable_reason, '') NOT LIKE 'manual%';
		END;
		$$;

