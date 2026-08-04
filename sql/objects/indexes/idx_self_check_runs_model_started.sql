--
-- Name: idx_self_check_runs_model_started; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_self_check_runs_model_started ON public.self_check_runs USING btree (model_name, started_at DESC);

