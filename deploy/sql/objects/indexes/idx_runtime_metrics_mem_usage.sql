--
-- Name: idx_runtime_metrics_mem_usage; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_metrics_mem_usage ON public.runtime_metrics USING btree (mem_used_mb);

