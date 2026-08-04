--
-- Name: idx_self_check_runs_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_self_check_runs_started ON public.self_check_runs USING btree (started_at DESC);

