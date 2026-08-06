--
-- Name: session_module_executions_hot chk_sme_hot_status; Type: CHECK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE public.session_module_executions_hot
    ADD CONSTRAINT chk_sme_hot_status CHECK (((status)::text = ANY (ARRAY[('pending'::character varying)::text, ('running'::character varying)::text, ('completed'::character varying)::text, ('failed'::character varying)::text, ('skipped'::character varying)::text]))) NOT VALID;

