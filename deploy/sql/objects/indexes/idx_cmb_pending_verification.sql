--
-- Name: idx_cmb_pending_verification; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cmb_pending_verification ON public.credential_model_bindings USING btree (credential_id) WHERE (pending_verification = true);

