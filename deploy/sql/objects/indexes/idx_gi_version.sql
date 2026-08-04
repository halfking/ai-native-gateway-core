--
-- Name: idx_gi_version; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_version ON public.gateway_instances USING btree (version);

