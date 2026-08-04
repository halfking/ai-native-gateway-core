--
-- Name: idx_request_logs_hot_tenant_ts; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_request_logs_hot_tenant_ts ON public.request_logs_hot USING btree (tenant_id, ts DESC);

