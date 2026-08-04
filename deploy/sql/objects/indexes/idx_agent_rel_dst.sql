--
-- Name: idx_agent_rel_dst; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_agent_rel_dst ON public.agent_relationships USING btree (dst_agent_id);

