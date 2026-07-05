--
-- Name: credential_model_peak_1m_bucket_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX credential_model_peak_1m_bucket_idx ON public.credential_model_peak_1m USING btree (bucket DESC);

