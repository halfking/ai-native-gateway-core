--
-- Name: idx_stats_1m_model_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_stats_1m_model_bucket ON public.credential_model_stats_1m USING btree (raw_model, bucket DESC);

