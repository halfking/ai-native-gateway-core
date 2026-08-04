--
-- Name: idx_gi_region; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_region ON public.gateway_instances USING btree (region);

