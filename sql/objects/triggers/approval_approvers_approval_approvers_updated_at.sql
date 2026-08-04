--
-- Name: approval_approvers approval_approvers_updated_at; Type: TRIGGER; Schema: public; Owner: -
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
				    FROM request_logs_hot
				    WHERE credential_id = p_credential_id
				      AND lower(COALESCE(outbound_model, client_model)) = lower(p_raw_model)
				      AND ts > NOW() - (p_window_hours || ' hours')::interval
				      -- Probe/self-check rows measure the health worker, not the
				      -- business route. Legacy probe IDs are retained for old rows
				      -- created before origin_stage/task_type was added.
				      AND COALESCE(task_type, '') <> 'probe_triggered'
				      AND COALESCE(origin_stage, '') NOT IN ('self_check', 'node_probe', 'system_health')
				      AND request_id NOT LIKE 'probe-%'
				    ORDER BY ts DESC
				    LIMIT p_sample_n
			    )

		    SELECT AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END)::double precision,
		           COUNT(*)::int
		    FROM recent;
		$$;

