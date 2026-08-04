--
-- Name: v_adaptive_probe_targets; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_adaptive_probe_targets AS
 SELECT cmb.id AS binding_id,
    cmb.credential_id,
    pm.raw_model_name,
    COALESCE(mps.consecutive_failures, 0) AS consecutive_failures,
    COALESCE(mps.consecutive_successes, 0) AS consecutive_successes,
    COALESCE(mps.state, 'unknown'::text) AS probe_state,
    mps.last_attempt_at,
    mps.next_retry_at,
    EXTRACT(epoch FROM (now() - COALESCE(mps.last_attempt_at, (now() - '01:00:00'::interval)))) AS age_secs,
    ( SELECT count(*) AS count
           FROM public.candidate_failure_logs cfl
          WHERE ((cfl.credential_id = cmb.credential_id) AND (cfl.raw_model_name = pm.raw_model_name) AND (cfl.ts > (now() - '00:05:00'::interval)))) AS recent_passive_failures
   FROM ((((public.credential_model_bindings cmb
     JOIN public.provider_models pm ON ((pm.id = cmb.provider_model_id)))
     JOIN public.credentials c ON ((c.id = cmb.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
     LEFT JOIN public.model_probe_state mps ON (((mps.credential_id = cmb.credential_id) AND (mps.raw_model_name = pm.raw_model_name))))
  WHERE ((COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.availability_state, 'ready'::text) <> 'suspended'::text) AND (COALESCE(c.quota_state, 'ok'::text) <> ALL (ARRAY['permanently_exhausted'::text, 'balance_exhausted'::text])) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(cmb.unavailable_reason, ''::text) !~~ 'manual%'::text) AND (COALESCE(mps.state, 'unknown'::text) <> 'broken_confirmed'::text));


--
-- Name: VIEW v_adaptive_probe_targets; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_adaptive_probe_targets IS 'Per-(cred, model) row with adaptive scheduling fields (age, recent failures). The model probe runner selects from this view, ordered by urgency.';

