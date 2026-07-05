--
-- Name: idx_api_keys_tier; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_tier ON public.api_keys USING btree (key_tier);

