--
-- Name: session_analysis_metadata; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.session_analysis_metadata (
    tenant_id text NOT NULL,
    scoped_session_id text NOT NULL,
    schema_version text DEFAULT 'session-analysis/v1'::text NOT NULL,
    status text NOT NULL,
    input_hash text NOT NULL,
    payload jsonb NOT NULL,
    source_task_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT session_analysis_metadata_status_check CHECK ((status = ANY (ARRAY['provisional'::text, 'final'::text])))
);

--
-- Name: session_analysis_metadata session_analysis_metadata_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.session_analysis_metadata
    ADD CONSTRAINT session_analysis_metadata_pkey PRIMARY KEY (tenant_id, scoped_session_id, status);

--
-- Name: idx_session_analysis_metadata_tenant_updated; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_session_analysis_metadata_tenant_updated ON public.session_analysis_metadata USING btree (tenant_id, updated_at DESC);
