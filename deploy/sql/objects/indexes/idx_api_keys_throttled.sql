--
-- Name: idx_api_keys_throttled; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_throttled ON public.api_keys USING btree (throttled_at) WHERE (throttled_at IS NOT NULL);

