--
-- Name: idx_credentials_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_tenant ON public.credentials USING btree (tenant_id);

