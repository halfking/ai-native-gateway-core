--
-- Name: idx_usage_minute_credential_bucket; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_usage_minute_credential_bucket ON public.usage_minute USING btree (credential_id, bucket DESC);

