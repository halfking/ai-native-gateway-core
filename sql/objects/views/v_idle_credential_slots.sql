--
-- Name: v_idle_credential_slots; Type: VIEW; Schema: public; Owner: -
--

CREATE VIEW public.v_idle_credential_slots AS
 SELECT credential_id,
    raw_model_name,
    state,
    consecutive_failures,
    last_attempt_at,
    (EXTRACT(epoch FROM (now() - last_attempt_at)))::integer AS idle_seconds
   FROM public.model_probe_state
  WHERE (state <> 'broken_confirmed'::text);


--
-- Name: VIEW v_idle_credential_slots; Type: COMMENT; Schema: public; Owner: -
--

COMMENT ON VIEW public.v_idle_credential_slots IS 'For monitoring: per-binding rows with last_attempt_at and idle_seconds. Used by admin dashboards to spot slots that need reclaim.';

