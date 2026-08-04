--
-- Name: idx_mpr_hot_created_at; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_hot_created_at ON public.model_probe_runs_hot USING btree (created_at DESC);

