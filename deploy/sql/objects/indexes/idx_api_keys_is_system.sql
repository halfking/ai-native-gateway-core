--
-- Name: idx_api_keys_is_system; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_is_system ON public.api_keys USING btree (is_system) WHERE (is_system = true);

