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

