--
-- Name: session_module_executions chk_sme_status; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.session_module_executions
    ADD CONSTRAINT chk_sme_status CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('running'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('skipped'::character varying)::text]))) NOT VALID;

