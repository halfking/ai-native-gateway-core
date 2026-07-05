--
-- Name: idx_stats_1m_cred_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stats_1m_cred_bucket ON public.credential_model_stats_1m USING btree (credential_id, bucket DESC);

