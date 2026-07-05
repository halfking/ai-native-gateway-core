--
-- Name: idx_tool_registry_tenant_tool; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_tenant_tool ON public.tool_registry USING btree (tenant_id, tool_id, version DESC);

