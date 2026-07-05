--
-- Name: idx_usage_minute_canonical_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_canonical_bucket ON public.usage_minute USING btree (canonical_id, bucket DESC);

