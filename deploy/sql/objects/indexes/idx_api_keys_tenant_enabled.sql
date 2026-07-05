--
-- Name: idx_api_keys_tenant_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_tenant_enabled ON public.api_keys USING btree (tenant_id, enabled);

