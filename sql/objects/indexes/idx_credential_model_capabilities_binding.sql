--
-- Name: idx_credential_model_capabilities_binding; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_credential_model_capabilities_binding ON public.credential_model_capabilities USING btree (credential_model_binding_id) WHERE (supported = true);
