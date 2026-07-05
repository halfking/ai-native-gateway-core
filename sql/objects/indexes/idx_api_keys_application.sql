--
-- Name: idx_api_keys_application; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_api_keys_application ON public.api_keys USING btree (application_id);

