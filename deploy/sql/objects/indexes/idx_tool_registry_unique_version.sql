--
-- Name: idx_tool_registry_unique_version; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_tool_registry_unique_version ON public.tool_registry USING btree (tenant_id, tool_id, version);

