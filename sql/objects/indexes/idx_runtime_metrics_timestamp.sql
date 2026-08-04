--
-- Name: idx_runtime_metrics_timestamp; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_runtime_metrics_timestamp ON public.runtime_metrics USING btree ("timestamp" DESC);

