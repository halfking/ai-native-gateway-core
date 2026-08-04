--
-- Name: idx_self_check_runs_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_self_check_runs_model ON public.self_check_runs USING btree (model_name);

