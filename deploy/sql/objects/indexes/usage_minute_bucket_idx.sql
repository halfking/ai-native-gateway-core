--
-- Name: usage_minute_bucket_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX usage_minute_bucket_idx ON public.usage_minute USING btree (bucket DESC);

