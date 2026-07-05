--
-- Name: idx_key_applications_fingerprint; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_key_applications_fingerprint ON public.key_applications USING btree (fingerprint, status);

