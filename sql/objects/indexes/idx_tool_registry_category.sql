--
-- Name: idx_tool_registry_category; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_tool_registry_category ON public.tool_registry USING btree (category) WHERE (enabled = true);

