--
-- Name: idx_runtime_metrics_cpu_high; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_metrics_cpu_high ON public.runtime_metrics USING btree ("timestamp" DESC) WHERE (cpu_usage_pct > (80)::double precision);

