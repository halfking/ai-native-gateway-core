--
-- Name: idx_agents_capabilities; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_capabilities ON public.agents USING gin (capabilities jsonb_path_ops);

