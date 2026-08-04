--
-- Name: idx_diagnostic_runs_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_state ON public.diagnostic_runs USING btree (state) WHERE (state = ANY (ARRAY['pending'::text, 'running'::text]));

