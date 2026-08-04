--
-- Name: idx_pct_model; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_pct_model ON public.provider_credibility_tests USING btree (model_name, test_time DESC);

