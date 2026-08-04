--
-- Name: idx_runtime_metrics_instance; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_metrics_instance ON public.runtime_metrics USING btree (instance_id, "timestamp" DESC);

