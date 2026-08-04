--
-- Name: idx_pct_credential_time; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pct_credential_time ON public.provider_credibility_tests USING btree (credential_id, test_time DESC);

