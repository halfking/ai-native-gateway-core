--
-- Name: idx_credentials_default_probe_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credentials_default_probe_model ON public.credentials USING btree (default_probe_model) WHERE (default_probe_model IS NOT NULL);

