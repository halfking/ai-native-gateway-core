--
-- Name: idx_tenant_tool_policies_tenant; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_tool_policies_tenant ON public.tenant_tool_policies USING btree (tenant_id) WHERE (enabled = true);

