--
-- Name: get_model_state_summary(text); Type: FUNCTION; Schema: public; Owner: -
--

CREATE FUNCTION public.get_model_state_summary(p_raw_model_name text) RETURNS TABLE(state text, priority text, count bigint, avg_success_rate numeric, next_probe_in_seconds integer)
    LANGUAGE sql STABLE
    AS $$
		    SELECT
		        sub.state::TEXT,
		        sub.priority::TEXT,
		        COUNT(*) as count,
		        ROUND(AVG(CASE WHEN sub.total_attempts > 0
		                       THEN sub.consecutive_successes::float / sub.total_attempts * 100
		                       ELSE NULL END)::numeric, 2) as avg_success_rate,
		        EXTRACT(EPOCH FROM MIN(sub.next_retry_at - NOW()))::INTEGER as next_probe_in_seconds
		    FROM (
		        SELECT
		            mps.state,
		            mps.consecutive_successes,
		            mps.total_attempts,
		            mps.next_retry_at,
		            CASE
		                WHEN mps.consecutive_failures >= 3 THEN 'urgent'
		                WHEN mps.state = 'suspicious' THEN 'suspicious'
		                WHEN mps.state IN ('failing', 'recovering') THEN 'failing'
		                ELSE 'watchdog'
		            END as priority
		        FROM model_probe_state mps
		        JOIN credentials c ON c.id = mps.credential_id
		        WHERE mps.raw_model_name = p_raw_model_name
		          AND COALESCE(c.status, 'active') = 'active'
		          AND COALESCE(c.lifecycle_status, 'active') = 'active'
		          AND COALESCE(c.manual_disabled, FALSE) = FALSE
		    ) sub
		    GROUP BY sub.state, sub.priority
		    ORDER BY
		        CASE sub.priority
		            WHEN 'urgent' THEN 1
		            WHEN 'suspicious' THEN 2
		            WHEN 'failing' THEN 3
		            WHEN 'watchdog' THEN 4
		            ELSE 5
		        END,
		        sub.state;
		$$;


SET default_table_access_method = heap;
