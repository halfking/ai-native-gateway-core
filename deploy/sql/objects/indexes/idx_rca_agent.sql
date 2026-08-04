--
-- Name: idx_rca_agent; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_rca_agent ON public.request_context_attrs USING btree (agent_name);

