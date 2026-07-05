--
-- Name: idx_api_keys_kid; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_kid ON public.api_keys USING btree (key_ciphertext_kid) WHERE (key_ciphertext_kid IS NOT NULL);

