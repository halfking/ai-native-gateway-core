--
-- Name: idx_rsm_tenant_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rsm_tenant_bucket ON public.request_stats_minute USING btree (tenant_id, bucket DESC);

