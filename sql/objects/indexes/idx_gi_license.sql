--
-- Name: idx_gi_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_gi_license ON public.gateway_instances USING btree (license_key_hash);

