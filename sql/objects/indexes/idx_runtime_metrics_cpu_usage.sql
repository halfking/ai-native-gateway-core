--
-- Name: idx_runtime_metrics_cpu_usage; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_metrics_cpu_usage ON public.runtime_metrics USING btree (cpu_usage_pct);

