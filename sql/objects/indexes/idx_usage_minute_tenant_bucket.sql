--
-- Name: idx_usage_minute_tenant_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_tenant_bucket ON public.usage_minute USING btree (tenant_id, bucket DESC);

