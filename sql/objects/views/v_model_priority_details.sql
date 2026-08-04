--
-- Name: v_model_priority_details; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_model_priority_details AS
 SELECT mps.raw_model_name,
    mps.raw_model_name AS outbound_model_name,
        CASE
            WHEN (mps.consecutive_failures >= 3) THEN 'urgent'::text
            WHEN (mps.state = 'suspicious'::text) THEN 'suspicious'::text
            WHEN (mps.state = ANY (ARRAY['failing'::text, 'recovering'::text])) THEN 'failing'::text
            ELSE 'watchdog'::text
        END AS probe_priority,
    mps.state,
    c.id AS credential_id,
    c.label AS credential_label,
    p.display_name AS provider_name,
    mps.last_attempt_at AS last_verified_at,
    mps.next_retry_at,
    mps.last_attempt_at AS marked_suspicious_at,
    NULL::timestamp without time zone AS probing_started_at,
    mps.consecutive_successes,
    mps.consecutive_failures,
    0 AS consecutive_watchdog_successes,
        CASE
            WHEN (mps.total_attempts > 0) THEN (((mps.consecutive_successes)::double precision / (mps.total_attempts)::double precision) * (100)::double precision)
            ELSE NULL::double precision
        END AS success_rate_7d,
    (mps.next_retry_at - now()) AS verification_interval,
    0 AS real_success_24h,
    0 AS real_failure_24h,
    mps.last_attempt_at AS last_real_request_at,
    NULL::text AS last_unavailable_reason,
    mps.last_status AS last_err_code,
        CASE
            WHEN (mps.next_retry_at <= now()) THEN 'ready'::text
            WHEN (mps.next_retry_at <= (now() + '00:01:00'::interval)) THEN '<1min'::text
            WHEN (mps.next_retry_at <= (now() + '00:05:00'::interval)) THEN '<5min'::text
            WHEN (mps.next_retry_at <= (now() + '01:00:00'::interval)) THEN '<1h'::text
            ELSE '>1h'::text
        END AS retry_in,
    (EXTRACT(epoch FROM (now() - mps.last_attempt_at)) / (60)::numeric) AS state_duration_minutes
   FROM ((public.model_probe_state mps
     JOIN public.credentials c ON ((c.id = mps.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
  WHERE ((COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false))
  ORDER BY mps.raw_model_name,
        CASE
            WHEN (mps.consecutive_failures >= 3) THEN 1
            WHEN (mps.state = 'suspicious'::text) THEN 2
            WHEN (mps.state = ANY (ARRAY['failing'::text, 'recovering'::text])) THEN 3
            ELSE 4
        END, c.id;

