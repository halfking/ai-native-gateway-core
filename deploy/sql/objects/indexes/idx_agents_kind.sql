--
-- Name: idx_agents_kind; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agents_kind ON public.agents USING btree (tenant_id, kind);

