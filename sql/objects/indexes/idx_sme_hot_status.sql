--
-- Name: idx_sme_hot_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_sme_hot_status ON public.session_module_executions_hot USING btree (status, started_at) WHERE ((status)::text = ANY (ARRAY[('running'::character varying)::text, ('failed'::character varying)::text]));

