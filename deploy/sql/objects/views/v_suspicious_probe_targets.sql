--
-- Name: v_suspicious_probe_targets; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_suspicious_probe_targets AS
 SELECT mps.credential_id,
    pm.raw_model_name,
    COALESCE(pm.outbound_model_name, ''::text) AS outbound_model_name,
    COALESCE(p.base_url, ''::text) AS base_url,
    COALESCE(p.protocol, 'openai-completions'::text) AS protocol,
    mps.marked_suspicious_at,
    mps.next_retry_at,
    mps.consecutive_failures,
    mps.consecutive_successes,
    public.model_probe_credential_concurrency(mps.credential_id) AS credential_probe_count
   FROM (((public.model_probe_state mps
     JOIN public.credentials c ON ((c.id = mps.credential_id)))
     JOIN public.providers p ON ((p.id = c.provider_id)))
      JOIN public.provider_models pm ON (((pm.raw_model_name = mps.raw_model_name) AND (EXISTS ( SELECT 1
            FROM public.credential_model_bindings cmb
           WHERE ((cmb.credential_id = mps.credential_id) AND (cmb.provider_model_id = pm.id) AND (COALESCE(cmb.admin_protected, false) = false)))))))
  WHERE ((mps.state = 'suspicious'::text) AND (mps.next_retry_at <= now()) AND (COALESCE(c.status, 'active'::text) = 'active'::text) AND (COALESCE(c.lifecycle_status, 'active'::text) = 'active'::text) AND (COALESCE(c.manual_disabled, false) = false) AND (COALESCE(p.enabled, false) = true) AND (COALESCE(p.manual_disabled, false) = false) AND (public.model_probe_credential_concurrency(mps.credential_id) < 2))
  ORDER BY (public.model_probe_credential_concurrency(mps.credential_id)), mps.marked_suspicious_at, mps.next_retry_at
 LIMIT 100;

