--
-- Name: idx_api_keys_status; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_status ON public.api_keys USING btree (status);

