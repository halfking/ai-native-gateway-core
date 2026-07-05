--
-- Name: idx_providers_tenant_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_providers_tenant_enabled ON public.providers USING btree (tenant_id, enabled);

