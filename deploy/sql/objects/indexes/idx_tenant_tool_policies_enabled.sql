--
-- Name: idx_tenant_tool_policies_enabled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tenant_tool_policies_enabled ON public.tenant_tool_policies USING btree (enabled);

