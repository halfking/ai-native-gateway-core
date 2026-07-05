--
-- Name: idx_mpr_model_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_model_created ON public.model_probe_runs USING btree (raw_model_name, created_at DESC);

