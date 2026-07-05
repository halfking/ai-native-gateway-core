--
-- Name: idx_envelope_credential; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_envelope_credential ON public.request_envelope USING btree (credential_id);

