--
-- Name: idx_pct_provider; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pct_provider ON public.provider_credibility_tests USING btree (provider_id, test_time DESC);

