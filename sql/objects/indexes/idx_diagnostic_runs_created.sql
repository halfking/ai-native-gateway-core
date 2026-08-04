--
-- Name: idx_diagnostic_runs_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_created ON public.diagnostic_runs USING btree (created_at DESC);

