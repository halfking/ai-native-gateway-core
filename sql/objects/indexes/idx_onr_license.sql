--
-- Name: idx_onr_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_onr_license ON public.ops_node_registrations USING btree (license_key);

