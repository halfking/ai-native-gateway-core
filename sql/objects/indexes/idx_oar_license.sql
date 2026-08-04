--
-- Name: idx_oar_license; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_oar_license ON public.offline_activation_requests USING btree (license_key);

