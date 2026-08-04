--
-- Name: idx_cmb_credential_provider_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_credential_provider_model ON public.credential_model_bindings USING btree (credential_id, provider_model_id);

