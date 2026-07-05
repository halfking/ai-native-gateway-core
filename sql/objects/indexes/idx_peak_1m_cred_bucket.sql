--
-- Name: idx_peak_1m_cred_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_peak_1m_cred_bucket ON public.credential_model_peak_1m USING btree (credential_id, bucket DESC);

