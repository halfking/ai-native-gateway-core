--
-- Name: dashboard_access_events chk_dae_event_type; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.dashboard_access_events
    ADD CONSTRAINT chk_dae_event_type CHECK (((event_type)::text = ANY (ARRAY[('api_access'::character varying)::text, ('query'::character varying)::text, ('export'::character varying)::text, ('error'::character varying)::text]))) NOT VALID;

