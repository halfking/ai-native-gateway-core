--
-- Name: idx_mpr_hot_status_created; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_mpr_hot_status_created ON public.model_probe_runs_hot USING btree (status, created_at DESC) WHERE (status <> 'ok'::text);

