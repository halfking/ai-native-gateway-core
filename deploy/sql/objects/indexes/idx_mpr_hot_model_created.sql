--
-- Name: idx_mpr_hot_model_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_hot_model_created ON public.model_probe_runs_hot USING btree (raw_model_name, created_at DESC);

