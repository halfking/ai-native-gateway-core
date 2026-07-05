--
-- Name: idx_cmb_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_credential ON public.credential_model_bindings USING btree (credential_id);

