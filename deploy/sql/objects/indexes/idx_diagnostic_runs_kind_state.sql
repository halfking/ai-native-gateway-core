--
-- Name: idx_diagnostic_runs_kind_state; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_diagnostic_runs_kind_state ON public.diagnostic_runs USING btree (kind, state, started_at DESC);

