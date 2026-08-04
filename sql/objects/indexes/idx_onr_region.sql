--
-- Name: idx_onr_region; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_onr_region ON public.ops_node_registrations USING btree (region, status);

