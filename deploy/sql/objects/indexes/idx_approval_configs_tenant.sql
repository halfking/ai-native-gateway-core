--
-- Name: idx_approval_configs_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_approval_configs_tenant ON public.approval_configs USING btree (tenant_id);

